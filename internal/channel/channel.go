// Package channel 从已解析的供应商配置派生渠道，不另建路由表。
// 渠道看模型 API 的目标主机名。出网代理、结果下载 CDN 和 STS 不另算渠道。
// 实例键只在配置里改名；这里给出应使用的新键，供本地改写和观测使用。
package channel

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	// 与 google.golang.org/genai v1.43.0 在未设置 BaseURL 时的主机一致。
	// Gemini Developer API 走 generativelanguage；Vertex ADC 的 global 客户端走不带区域前缀的 aiplatform。
	geminiDefaultHost = "generativelanguage.googleapis.com"
	vertexDefaultHost = "aiplatform.googleapis.com"

	// 下列缺省主机与对应适配器的默认地址一致，只取主机名，路径不参与渠道。
	openaiDefaultHost     = "api.openai.com"
	arkDefaultHost        = "ark.cn-beijing.volces.com"
	dashscopeDefaultHost  = "dashscope.aliyuncs.com"
	ominilinkVideoHost    = "vg-api.aig-ai.com"
	photoroomDefaultHost  = "sdk.photoroom.com"
	elevenLabsDefaultHost = "api.elevenlabs.io"
	minimaxDefaultHost    = "api.minimaxi.com"
	volcengineDefaultHost = "openspeech.bytedance.com"
	assemblyAIDefaultHost = "streaming.assemblyai.com"
	deepgramDefaultHost   = "api.deepgram.com"
	sonioxDefaultHost     = "stt-rt.soniox.com"
	tencentDefaultHost    = "asr.cloud.tencent.com"
	aliyunNLSDefaultHost  = "nls-gateway.cn-shanghai.aliyuncs.com"
	maasBeijingSuffix     = ".cn-beijing.maas.aliyuncs.com"
	channelWeiguang       = "薇光点亮"
	instanceSegmentPerson = "wgdl_segment_person"
	instanceWeiguangImage = "wgdl_image"
	instanceWeiguangVideo = "wgdl_video"
	instanceHumanParser   = "wgdl_human_parser"
)

// Identity 是一个实例的渠道事实。Host 是模型 API 主机，不含端口、路径和代理。
// instance 只在薇光点亮模型族命中时有值。Names 使用 Of 已经算出的实例键。
type Identity struct {
	Protocol string
	Host     string
	Channel  string
	instance string
}

// Of 按该实例唯一适配器计算渠道。代理字段被读到后丢弃，避免把出口代理当成供应商。
func Of(provider config.ProviderConfig) (Identity, error) {
	protocol, raw, err := endpoint(provider)
	if err != nil {
		return Identity{}, err
	}
	host, err := hostname(raw)
	if err != nil {
		return Identity{}, err
	}
	channelName := displayName(host)
	instance, err := weiguangInstance(provider, protocol)
	if err != nil {
		return Identity{}, err
	}
	if instance != "" {
		// 薇光点亮模型跨主机都算薇光点亮。主机仍保留，其他自建服务按自己的主机归渠道。
		channelName = channelWeiguang
	}
	return Identity{Protocol: protocol, Host: host, Channel: channelName, instance: instance}, nil
}

// Names 给出本份配置里每个旧实例键应改成的新键。
// 同一渠道只有一个实例时用渠道 slug。同一渠道多种协议时加协议后缀。
// 同一协议有多份时，再加能区分用途的尾巴。薇光点亮实例固定为 wgdl_*，不用中文键。
// 重复计算得到同一结果，便于对已经改过的文件再跑一次。
func Names(providers map[string]config.ProviderConfig) (map[string]string, error) {
	type groupKey struct {
		channel  string
		protocol string
	}
	idents := make(map[string]Identity, len(providers))
	channelCount := map[string]int{}
	groups := map[groupKey][]string{}
	for name, provider := range providers {
		id, err := Of(provider)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", name, err)
		}
		idents[name] = id
		channelCount[id.Channel]++
		key := groupKey{id.Channel, id.Protocol}
		groups[key] = append(groups[key], name)
	}
	out := make(map[string]string, len(providers))
	for key, members := range groups {
		sort.Strings(members)
		slug := strings.ReplaceAll(key.channel, " ", "_")
		suffix := protocolSuffix(key.protocol, slug)
		var roles map[string]string
		if len(members) > 1 {
			roles = siblingRoles(members)
		}
		for _, name := range members {
			if idents[name].instance != "" {
				out[name] = idents[name].instance
				continue
			}
			if channelCount[key.channel] == 1 {
				out[name] = slug
				continue
			}
			role := ""
			if len(members) > 1 {
				role = roles[name]
			} else {
				role = singletonRole(name, slug, key.protocol)
			}
			out[name] = joinName(slug, suffix, role)
		}
	}
	claimed := map[string]string{}
	for old, name := range out {
		if strings.Contains(name, " ") || name == "" {
			return nil, fmt.Errorf("provider %s resolved to invalid instance name %q", old, name)
		}
		if prev, ok := claimed[name]; ok {
			return nil, fmt.Errorf("instance name %q is claimed by %s and %s", name, prev, old)
		}
		claimed[name] = old
	}
	return out, nil
}

func joinName(slug, suffix, role string) string {
	parts := []string{}
	if suffix != "" {
		parts = append(parts, suffix)
	}
	if role != "" {
		parts = append(parts, role)
	}
	if len(parts) == 0 {
		return slug
	}
	return slug + "_" + strings.Join(parts, "_")
}

// protocolSuffix 去掉协议名里已经出现在渠道 slug 中的前缀，避免 Cohere_cohere、rekognition_rekognition 这类重复。
func protocolSuffix(protocol, slug string) string {
	tokens := splitTokens(slug)
	glued := strings.ReplaceAll(slug, "_", "")
	parts := splitTokens(protocol)
	for len(parts) > 0 && tokenInChannel(parts[0], tokens, glued) {
		parts = parts[1:]
	}
	return strings.Join(parts, "_")
}

func tokenInChannel(part string, tokens []string, glued string) bool {
	for _, token := range tokens {
		if strings.EqualFold(part, token) {
			return true
		}
	}
	// vertexai 与渠道名 Vertex_AI 连写后相同，也算重复尾巴。
	return strings.EqualFold(part, glued)
}

func siblingRoles(names []string) map[string]string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	prefix := sorted[0]
	for _, name := range sorted[1:] {
		limit := len(prefix)
		if len(name) < limit {
			limit = len(name)
		}
		i := 0
		for i < limit && prefix[i] == name[i] {
			i++
		}
		prefix = prefix[:i]
	}
	exact := false
	for _, name := range sorted {
		if name == prefix {
			exact = true
			break
		}
	}
	if !exact {
		if i := strings.LastIndex(prefix, "_"); i >= 0 {
			prefix = prefix[:i+1]
		} else {
			prefix = ""
		}
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		role := name
		if prefix != "" && strings.HasPrefix(name, prefix) {
			role = strings.TrimPrefix(name[len(prefix):], "_")
		}
		out[name] = role
	}
	return out
}

// singletonRole 在本文件里没有同协议邻居时，仍保留旧键里真正区分用途的尾巴。
// 这样只有 dashscope_vl、没有 dashscope_chat 的环境也叫 阿里云百炼_openai_vl，与两份都在的环境一致。
// 旧键里的 cn/sg/us 是部署标记，渠道已经带了目标区域，不再写进新键。
func singletonRole(name, slug, protocol string) string {
	suffix := protocolSuffix(protocol, slug)
	base := joinName(slug, suffix, "")
	if name == base {
		return ""
	}
	if strings.HasPrefix(name, base+"_") {
		return strings.TrimPrefix(name, base+"_")
	}
	parts := splitTokens(name)
	if len(parts) == 0 {
		return ""
	}
	tokens := splitTokens(slug)
	glued := strings.ReplaceAll(slug, "_", "")
	protoTokens := splitTokens(protocol)
	if tokenInChannel(parts[0], tokens, glued) || (len(protoTokens) > 0 && strings.EqualFold(parts[0], protoTokens[0])) {
		return ""
	}
	tail := append([]string(nil), parts[1:]...)
	for len(tail) > 0 && isDeployTag(tail[len(tail)-1]) {
		tail = tail[:len(tail)-1]
	}
	channelTokens := append(append([]string{}, tokens...), protoTokens...)
	kept := make([]string, 0, len(tail))
	for _, part := range tail {
		if tokenInChannel(part, channelTokens, glued) {
			continue
		}
		kept = append(kept, part)
	}
	// 多段描述（如 gpt_image）不是同协议分身的短标记。只有 vl 这种单词才保留，避免唯一实例被旧键拖出第二套名字。
	if len(kept) != 1 {
		return ""
	}
	return kept[0]
}

func isDeployTag(token string) bool {
	switch token {
	case "cn", "sg", "us":
		return true
	default:
		return false
	}
}

func splitTokens(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, "_")
}

// weiguangInstance 只在模型属于点名的族时返回固定实例键。
// 族是 VWorld_*、segment-person-*、segment-subject-* 和 human_parse；旧 ltx ID 不再属于该族。
// VWorld_outfit_video 归视频，其余 VWorld_* 归生图。同前缀的新 ID 走同一条规则。
// 族确定之后，用协议选择 wgdl_segment_person、wgdl_image 或 wgdl_video。
func weiguangInstance(provider config.ProviderConfig, protocol string) (string, error) {
	kind, err := weiguangKind(provider.Models)
	if err != nil || kind == "" {
		return "", err
	}
	switch protocol {
	case "human_parser":
		if kind == "human_parse" {
			return instanceHumanParser, nil
		}
	case "segment_person":
		if kind == "segment" {
			return instanceSegmentPerson, nil
		}
	case "vworld_image":
		if kind == "image" {
			return instanceWeiguangImage, nil
		}
	case "ltx":
		if kind == "video" {
			return instanceWeiguangVideo, nil
		}
	}
	return "", fmt.Errorf("weiguang models require separate instances")
}

func weiguangKind(list []string) (string, error) {
	kind := ""
	for _, model := range list {
		next := familyKind(strings.TrimSpace(model))
		if next == "" {
			continue
		}
		if kind != "" && kind != next {
			return "", fmt.Errorf("weiguang models require separate instances")
		}
		kind = next
	}
	return kind, nil
}

func familyKind(model string) string {
	switch {
	case model == models.HumanParse:
		// G7 人体解析归属薇光点亮；只匹配此 ID，避免把旧 human-parser 一并改归属。
		return "human_parse"
	case strings.HasPrefix(model, "segment-person-"), strings.HasPrefix(model, "segment-subject-"):
		return "segment"
	case strings.HasPrefix(model, "VWorld_"):
		if strings.Contains(model, "outfit_video") {
			return "video"
		}
		return "image"
	default:
		return ""
	}
}

func endpoint(provider config.ProviderConfig) (protocol, raw string, err error) {
	switch {
	case provider.Gemini != nil:
		return "gemini", firstNonEmpty(provider.Gemini.BaseURL, "https://"+geminiDefaultHost), nil
	case provider.VertexAI != nil:
		// proxy_url 只影响出口，SDK 目标仍是 aiplatform.googleapis.com。本配置没有 location，不会变成 {region}-aiplatform。
		return "vertexai", "https://" + vertexDefaultHost, nil
	case provider.Ark != nil:
		return "ark", firstNonEmpty(provider.Ark.BaseURL, "https://"+arkDefaultHost+"/api/v3"), nil
	case provider.OpenAI != nil:
		return "openai", firstNonEmpty(provider.OpenAI.BaseURL, "https://"+openaiDefaultHost+"/v1"), nil
	case provider.LTX != nil:
		if strings.TrimSpace(provider.LTX.BaseURL) == "" {
			return "", "", fmt.Errorf("ltx base_url is required")
		}
		return "ltx", provider.LTX.BaseURL, nil
	case provider.DashScopeVideo != nil:
		return "dashscope_video", firstNonEmpty(provider.DashScopeVideo.BaseURL, "https://"+dashscopeDefaultHost+"/api/v1"), nil
	case provider.OminilinkVideo != nil:
		// 提交主机缺省是 vg-api。下载 CDN download-vod.aig-ai.com 不是这个实例的渠道。
		return "ominilink_video", firstNonEmpty(provider.OminilinkVideo.BaseURL, "https://"+ominilinkVideoHost+"/v1"), nil
	case provider.GeminiVideo != nil:
		return "gemini_video", firstNonEmpty(provider.GeminiVideo.BaseURL, "https://"+geminiDefaultHost+"/v1beta"), nil
	case provider.ArkVideo != nil:
		return "ark_video", firstNonEmpty(provider.ArkVideo.BaseURL, "https://"+arkDefaultHost+"/api/v3"), nil
	case provider.MinimaxTTS != nil:
		return "minimax_tts", firstNonEmpty(provider.MinimaxTTS.Endpoint, "wss://"+minimaxDefaultHost+"/ws/v1/t2a_v2"), nil
	case provider.ElevenLabsTTS != nil:
		return "elevenlabs_tts", firstNonEmpty(provider.ElevenLabsTTS.BaseURL, "https://"+elevenLabsDefaultHost), nil
	case provider.VolcengineASR != nil:
		return "volcengine_asr", firstNonEmpty(provider.VolcengineASR.URL, "wss://"+volcengineDefaultHost+"/api/v3/sauc/bigmodel_async"), nil
	case provider.AliyunASR != nil:
		// 未配置时用 NLS SDK DEFAULT_URL。深圳网关是显式 url，不能和上海缺省混成同一个主机。
		return "aliyun_asr", firstNonEmpty(provider.AliyunASR.URL, "wss://"+aliyunNLSDefaultHost+"/ws/v1"), nil
	case provider.FunASR != nil:
		return "fun_asr", asrWorkspaceOrDashScope(provider.FunASR.URL, provider.FunASR.WorkspaceID, "/api-ws/v1/inference"), nil
	case provider.QwenASR != nil:
		return "qwen_asr", asrWorkspaceOrDashScope(provider.QwenASR.URL, provider.QwenASR.WorkspaceID, "/api-ws/v1/realtime"), nil
	case provider.TencentASR != nil:
		// ProxyURL 是腾讯 SDK 的出网代理。识别主机固定 asr.cloud.tencent.com，不读取代理。
		return "tencent_asr", "wss://" + tencentDefaultHost, nil
	case provider.AssemblyAIASR != nil:
		return "assemblyai_asr", firstNonEmpty(provider.AssemblyAIASR.URL, "wss://"+assemblyAIDefaultHost+"/v3/ws"), nil
	case provider.DeepgramASR != nil:
		return "deepgram_asr", firstNonEmpty(provider.DeepgramASR.URL, "wss://"+deepgramDefaultHost+"/v1/listen"), nil
	case provider.SonioxASR != nil:
		return "soniox_asr", firstNonEmpty(provider.SonioxASR.URL, "wss://"+sonioxDefaultHost+"/transcribe-websocket"), nil
	case provider.Photoroom != nil:
		return "photoroom", firstNonEmpty(provider.Photoroom.BaseURL, "https://"+photoroomDefaultHost), nil
	case provider.SegmentPerson != nil:
		if strings.TrimSpace(provider.SegmentPerson.BaseURL) == "" {
			return "", "", fmt.Errorf("segment_person base_url is required")
		}
		return "segment_person", provider.SegmentPerson.BaseURL, nil
	case provider.VWorldImage != nil:
		if strings.TrimSpace(provider.VWorldImage.BaseURL) == "" {
			return "", "", fmt.Errorf("vworld_image base_url is required")
		}
		return "vworld_image", provider.VWorldImage.BaseURL, nil
	case provider.ClothYOLO != nil:
		if strings.TrimSpace(provider.ClothYOLO.BaseURL) == "" {
			return "", "", fmt.Errorf("cloth_yolo base_url is required")
		}
		return "cloth_yolo", provider.ClothYOLO.BaseURL, nil
	case provider.FaceSim != nil:
		if strings.TrimSpace(provider.FaceSim.BaseURL) == "" {
			return "", "", fmt.Errorf("facesim base_url is required")
		}
		return "facesim", provider.FaceSim.BaseURL, nil
	case provider.HumanYOLO != nil:
		if strings.TrimSpace(provider.HumanYOLO.BaseURL) == "" {
			return "", "", fmt.Errorf("human_yolo base_url is required")
		}
		return "human_yolo", provider.HumanYOLO.BaseURL, nil
	case provider.HumanParser != nil:
		if strings.TrimSpace(provider.HumanParser.BaseURL) == "" {
			return "", "", fmt.Errorf("human_parser base_url is required")
		}
		return "human_parser", provider.HumanParser.BaseURL, nil
	case provider.RekognitionDetect != nil:
		raw, err = awsRegional("rekognition", provider.RekognitionDetect.Region)
		return "rekognition_detect", raw, err
	case provider.FacebodyCompare != nil:
		if strings.TrimSpace(provider.FacebodyCompare.Endpoint) == "" {
			return "", "", fmt.Errorf("facebody_compare endpoint is required")
		}
		return "facebody_compare", provider.FacebodyCompare.Endpoint, nil
	case provider.RekognitionCompare != nil:
		raw, err = awsRegional("rekognition", provider.RekognitionCompare.Region)
		return "rekognition_compare", raw, err
	case provider.DashScopeEmbedding != nil:
		if strings.TrimSpace(provider.DashScopeEmbedding.BaseURL) == "" {
			return "", "", fmt.Errorf("dashscope_embedding base_url is required")
		}
		return "dashscope_embedding", provider.DashScopeEmbedding.BaseURL, nil
	case provider.CohereEmbedding != nil:
		if strings.TrimSpace(provider.CohereEmbedding.BaseURL) == "" {
			return "", "", fmt.Errorf("cohere_embedding base_url is required")
		}
		return "cohere_embedding", provider.CohereEmbedding.BaseURL, nil
	case provider.BedrockEmbedding != nil:
		// InvokeModel 走 bedrock-runtime。role_arn 触发的 STS 不是模型渠道。
		raw, err = awsRegional("bedrock-runtime", provider.BedrockEmbedding.Region)
		return "bedrock_embedding", raw, err
	case provider.DashScopeRerank != nil:
		if strings.TrimSpace(provider.DashScopeRerank.BaseURL) == "" {
			return "", "", fmt.Errorf("dashscope_rerank base_url is required")
		}
		return "dashscope_rerank", provider.DashScopeRerank.BaseURL, nil
	case provider.BedrockRerank != nil:
		// Rerank 走 bedrock-agent-runtime，区域来自配置，不看来源部署在哪个区域。
		raw, err = awsRegional("bedrock-agent-runtime", provider.BedrockRerank.Region)
		return "bedrock_rerank", raw, err
	case provider.CohereRerank != nil:
		if strings.TrimSpace(provider.CohereRerank.BaseURL) == "" {
			return "", "", fmt.Errorf("cohere_rerank base_url is required")
		}
		return "cohere_rerank", provider.CohereRerank.BaseURL, nil
	case provider.GoogleRerank != nil:
		// OAuth token 交换和出网代理不算模型渠道，目标始终是 Discovery Engine。
		return "google_rerank", "https://discoveryengine.googleapis.com", nil
	case provider.MixedbreadRerank != nil:
		if strings.TrimSpace(provider.MixedbreadRerank.BaseURL) == "" {
			return "", "", fmt.Errorf("mixedbread_rerank base_url is required")
		}
		return "mixedbread_rerank", provider.MixedbreadRerank.BaseURL, nil
	case provider.FacebodyDetect != nil:
		if strings.TrimSpace(provider.FacebodyDetect.Endpoint) == "" {
			return "", "", fmt.Errorf("facebody_detect endpoint is required")
		}
		return "facebody_detect", provider.FacebodyDetect.Endpoint, nil
	case provider.FacebodyLibrary != nil:
		if strings.TrimSpace(provider.FacebodyLibrary.Endpoint) == "" {
			return "", "", fmt.Errorf("facebody_library endpoint is required")
		}
		return "facebody_library", provider.FacebodyLibrary.Endpoint, nil
	case provider.RekognitionFaces != nil:
		raw, err = awsRegional("rekognition", provider.RekognitionFaces.Region)
		return "rekognition_faces", raw, err
	case provider.RekognitionLibrary != nil:
		raw, err = awsRegional("rekognition", provider.RekognitionLibrary.Region)
		return "rekognition_library", raw, err
	case provider.ImageSeg != nil:
		if strings.TrimSpace(provider.ImageSeg.Endpoint) == "" {
			return "", "", fmt.Errorf("image_seg endpoint is required")
		}
		return "image_seg", provider.ImageSeg.Endpoint, nil
	case provider.Fashion != nil:
		if strings.TrimSpace(provider.Fashion.BaseURL) == "" {
			return "", "", fmt.Errorf("fashion base_url is required")
		}
		return "fashion", provider.Fashion.BaseURL, nil
	default:
		return "", "", fmt.Errorf("provider has no concrete adapter")
	}
}

func asrWorkspaceOrDashScope(rawURL, workspace, path string) string {
	if strings.TrimSpace(rawURL) != "" {
		return rawURL
	}
	workspace = strings.TrimSpace(workspace)
	if workspace != "" {
		// 与 fun-asr / qwen-asr 客户端一致：有工作区且没写 url 时打工作区专属 MaaS 主机。
		return "wss://" + workspace + maasBeijingSuffix + path
	}
	return "wss://" + dashscopeDefaultHost + path
}

func awsRegional(service, region string) (string, error) {
	region = strings.TrimSpace(region)
	if region == "" {
		return "", fmt.Errorf("%s region is required", service)
	}
	return "https://" + service + "." + region + ".amazonaws.com", nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hostname(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty endpoint")
	}
	if strings.Contains(raw, "://") {
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			return normalizeHost(parsed.Hostname()), nil
		}
		// 示例里的工作区占位符含 ${}，标准 URL 解析会失败。客户端仍会按这段主机去连，渠道不能因此算不出来。
		raw = raw[strings.Index(raw, "://")+3:]
	}
	if slash := strings.Index(raw, "/"); slash >= 0 {
		raw = raw[:slash]
	}
	if host, _, err := net.SplitHostPort(raw); err == nil && host != "" {
		return normalizeHost(host), nil
	}
	if raw == "" {
		return "", fmt.Errorf("invalid endpoint")
	}
	return normalizeHost(raw), nil
}

func normalizeHost(host string) string {
	if strings.ContainsAny(host, "${}") {
		return host
	}
	return strings.ToLower(host)
}

func displayName(host string) string {
	if name, ok := knownHosts[host]; ok {
		return name
	}
	if name, ok := awsDisplay(host); ok {
		return name
	}
	if strings.HasSuffix(host, ".maas.aliyuncs.com") {
		workspace := strings.Split(host, ".")[0]
		if workspace != "" {
			return "阿里云 MaaS_" + workspace
		}
	}
	// 没有确认品牌的主机直接展示主机名，不用实例名猜测归属。
	return host
}

func awsDisplay(host string) (string, bool) {
	switch {
	case strings.HasPrefix(host, "rekognition.") && strings.HasSuffix(host, ".amazonaws.com"):
		region := strings.TrimSuffix(strings.TrimPrefix(host, "rekognition."), ".amazonaws.com")
		return "AWS Rekognition_" + region, region != ""
	case strings.HasPrefix(host, "bedrock-agent-runtime.") && strings.HasSuffix(host, ".amazonaws.com"):
		region := strings.TrimSuffix(strings.TrimPrefix(host, "bedrock-agent-runtime."), ".amazonaws.com")
		return "AWS Bedrock Agent Runtime_" + region, region != ""
	case strings.HasPrefix(host, "bedrock-runtime.") && strings.HasSuffix(host, ".amazonaws.com"):
		region := strings.TrimSuffix(strings.TrimPrefix(host, "bedrock-runtime."), ".amazonaws.com")
		return "AWS Bedrock Runtime_" + region, region != ""
	default:
		return "", false
	}
}

// knownHosts 是已确认的品牌。键是模型 API 主机，不是部署区域，也不是下载或 STS 主机。
var knownHosts = map[string]string{
	"api.aig-ai.com":                       "AIG",
	"vg-api.aig-ai.com":                    "AIG VG",
	"api.ominilink.ai":                     "Ominilink",
	"generativelanguage.googleapis.com":    "Gemini Developer API",
	"discoveryengine.googleapis.com":       "Vertex AI",
	"aiplatform.googleapis.com":            "Vertex AI",
	"dashscope.aliyuncs.com":               "阿里云百炼",
	"ark.cn-beijing.volces.com":            "火山方舟",
	"openspeech.bytedance.com":             "火山语音",
	"open.bigmodel.cn":                     "智谱",
	"api.minimaxi.com":                     "MiniMax",
	"sdk.photoroom.com":                    "Photoroom",
	"api.cohere.com":                       "Cohere",
	"api.elevenlabs.io":                    "ElevenLabs",
	"api.mixedbread.com":                   "Mixedbread",
	"api.deepgram.com":                     "Deepgram",
	"streaming.assemblyai.com":             "AssemblyAI",
	"facebody.cn-shanghai.aliyuncs.com":    "阿里云人脸人体",
	"imageseg.cn-shanghai.aliyuncs.com":    "阿里云图像分割",
	"nls-gateway-cn-shenzhen.aliyuncs.com": "阿里云语音",
	"asr.cloud.tencent.com":                "腾讯云语音",
}
