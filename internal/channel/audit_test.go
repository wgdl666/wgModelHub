package channel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	"gopkg.in/yaml.v3"
)

// 这些 JSON 是 2026-10-11 22:33 的脱敏清单，只有实例、模型、协议和主机。
// 不是可上线的完整配置，测试不得把它们写回任何配置源。

type auditProtocol struct {
	Protocol        string            `json:"protocol"`
	Hosts           map[string]string `json:"hosts"`
	ProxyConfigured bool              `json:"proxy_configured"`
}

type auditProvider struct {
	Instance  string          `json:"instance"`
	Models    []string        `json:"models"`
	Protocols []auditProtocol `json:"protocols"`
}

type auditSnapshot struct {
	Environment string          `json:"environment"`
	Providers   []auditProvider `json:"providers"`
	ModelRoutes map[string]any  `json:"model_routes"`
}

type auditRow struct {
	Environment      string   `json:"environment"`
	OldInstance      string   `json:"old_instance"`
	Channel          string   `json:"channel"`
	Host             string   `json:"host"`
	Protocol         string   `json:"protocol"`
	ProposedInstance string   `json:"proposed_instance"`
	Models           []string `json:"models"`
}

func TestAuditedInstancesMatchChannelAndHaveUniqueNames(t *testing.T) {
	rows := loadAuditRows(t)
	byEnv := map[string][]auditRow{}
	for _, row := range rows {
		// 保留历史快照原文，但旧 ltx ID 下线后按实际主机归类，不能继续期待薇光点亮别名。
		if row.Protocol == "ltx" && slices.Equal(row.Models, []string{"ltx"}) {
			row.Channel = row.Host
		}
		byEnv[row.Environment] = append(byEnv[row.Environment], row)
	}
	if len(rows) != 114 {
		t.Fatalf("rows=%d, want 114", len(rows))
	}
	wantEnv := map[string]int{"cn-dev": 34, "aws-sg-dev": 29, "aws-ohio-test": 23, "us-east-exhibition": 28}
	total := 0
	manifest := make([]map[string]any, 0, 114)
	for env, want := range wantEnv {
		snap := loadSnapshot(t, env+".json")
		if snap.Environment != env || len(snap.Providers) != want || len(byEnv[env]) != want {
			t.Fatalf("%s providers=%d rows=%d", env, len(snap.Providers), len(byEnv[env]))
		}
		total += len(snap.Providers)
		providers := map[string]config.ProviderConfig{}
		mapped := map[string]auditRow{}
		for _, row := range byEnv[env] {
			mapped[row.OldInstance] = row
		}
		for _, item := range snap.Providers {
			row, ok := mapped[item.Instance]
			if !ok {
				t.Fatalf("%s missing mapping for %s", env, item.Instance)
			}
			providers[item.Instance] = providerFromAudit(t, item, row)
		}
		names, err := Names(providers)
		if err != nil {
			t.Fatalf("%s names: %v", env, err)
		}
		seen := map[string]string{}
		renamedProviders := map[string]config.ProviderConfig{}
		for old, provider := range providers {
			id, err := Of(provider)
			if err != nil {
				t.Fatalf("%s %s: %v", env, old, err)
			}
			row := mapped[old]
			if id.Host != row.Host || id.Channel != row.Channel || id.Protocol != row.Protocol {
				t.Fatalf("%s %s identity=%+v mapped host=%s channel=%s protocol=%s", env, old, id, row.Host, row.Channel, row.Protocol)
			}
			if !reflect.DeepEqual(provider.Models, row.Models) {
				t.Fatalf("%s %s models changed", env, old)
			}
			next := names[old]
			if strings.Contains(next, " ") {
				t.Fatalf("%s %s name %q contains space", env, old, next)
			}
			if prev, ok := seen[next]; ok {
				t.Fatalf("%s name %q claimed by %s and %s", env, next, prev, old)
			}
			seen[next] = old
			renamedProviders[next] = provider
			assertCorrectedName(t, env, old, row, next)
			manifest = append(manifest, map[string]any{
				"environment":    env,
				"old_instance":   old,
				"instance":       next,
				"channel":        row.Channel,
				"host":           row.Host,
				"protocol":       row.Protocol,
				"models":         row.Models,
				"draft_instance": row.ProposedInstance,
			})
		}
		again, err := Names(renamedProviders)
		if err != nil {
			t.Fatalf("%s second pass: %v", env, err)
		}
		for name, next := range again {
			if name != next {
				t.Fatalf("%s second pass %s -> %s", env, name, next)
			}
		}
		assertRoutes(t, snap, names, providers)
	}
	if total != 114 {
		t.Fatalf("total=%d", total)
	}
	// 只在明确指定路径时写出脱敏改名清单。普通测试不写仓库外文件。
	out := os.Getenv("MODELHUB_RENAME_MANIFEST")
	if out == "" {
		return
	}
	sort.Slice(manifest, func(i, j int) bool {
		if manifest[i]["environment"] != manifest[j]["environment"] {
			return manifest[i]["environment"].(string) < manifest[j]["environment"].(string)
		}
		return manifest[i]["old_instance"].(string) < manifest[j]["old_instance"].(string)
	})
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertCorrectedName(t *testing.T, env, old string, row auditRow, got string) {
	t.Helper()
	switch row.Protocol {
	case "segment_person":
		if got != "wgdl_segment_person" {
			t.Fatalf("%s %s=%s", env, old, got)
		}
		return
	case "vworld_image":
		if got != "wgdl_image" {
			t.Fatalf("%s %s=%s", env, old, got)
		}
		return
	case "ltx":
		want := "wgdl_video"
		if slices.Equal(row.Models, []string{"ltx"}) {
			want = row.Host
		}
		if got != want {
			t.Fatalf("%s %s=%s", env, old, got)
		}
		return
	}
	// 唯一渠道直接用渠道名。Bedrock 原建议名多出来的 _bedrock_rerank 因此去掉。
	if row.ProposedInstance == "AWS_Bedrock_Agent_Runtime_us-east-1_bedrock_rerank" {
		if got != "AWS_Bedrock_Agent_Runtime_us-east-1" {
			t.Fatalf("%s %s=%s", env, old, got)
		}
		return
	}
	want := stripDuplicateSuffix(row.Channel, row.ProposedInstance)
	if got != want {
		t.Fatalf("%s %s got %s want %s (proposed %s)", env, old, got, want, row.ProposedInstance)
	}
}

func stripDuplicateSuffix(channelName, proposed string) string {
	slug := strings.ReplaceAll(channelName, " ", "_")
	if proposed == slug || !strings.HasPrefix(proposed, slug+"_") {
		return proposed
	}
	parts := strings.Split(strings.TrimPrefix(proposed, slug+"_"), "_")
	tokens := strings.Split(slug, "_")
	glued := strings.ReplaceAll(slug, "_", "")
	for len(parts) > 0 && tokenInChannel(parts[0], tokens, glued) {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return slug
	}
	return slug + "_" + strings.Join(parts, "_")
}

func assertRoutes(t *testing.T, snap auditSnapshot, names map[string]string, providers map[string]config.ProviderConfig) {
	t.Helper()
	body, err := yaml.Marshal(map[string]any{
		"providers":    providers,
		"model_routes": snap.ModelRoutes,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 快照路由是 JSON 形状。先确认引用都指向本环境实例，再按改名表核对顺序。
	for model, raw := range snap.ModelRoutes {
		switch value := raw.(type) {
		case string:
			if _, ok := names[value]; !ok {
				t.Fatalf("%s route %s -> missing %s", snap.Environment, model, value)
			}
		case map[string]any:
			def, _ := value["default"].(string)
			if _, ok := names[def]; !ok {
				t.Fatalf("%s route %s default %s missing", snap.Environment, model, def)
			}
			race, _ := value["race"].([]any)
			for i, item := range race {
				name, _ := item.(string)
				if _, ok := names[name]; !ok {
					t.Fatalf("%s route %s race[%d]=%s missing", snap.Environment, model, i, name)
				}
			}
		default:
			t.Fatalf("%s route %s has %T", snap.Environment, model, raw)
		}
	}
	var before config.Config
	if err := yaml.Unmarshal(body, &before); err != nil {
		t.Fatal(err)
	}
	rewritten, renames, err := RewriteYAML(body)
	if err != nil {
		t.Fatalf("%s rewrite: %v", snap.Environment, err)
	}
	var after config.Config
	if err := yaml.Unmarshal(rewritten, &after); err != nil {
		t.Fatal(err)
	}
	if len(before.Providers) != len(after.Providers) {
		t.Fatalf("%s provider count %d -> %d", snap.Environment, len(before.Providers), len(after.Providers))
	}
	for old, provider := range before.Providers {
		got, ok := after.Providers[renames[old]]
		if !ok {
			t.Fatalf("%s missing renamed %s", snap.Environment, old)
		}
		if !reflect.DeepEqual(provider, got) {
			t.Fatalf("%s %s config changed besides the key", snap.Environment, old)
		}
	}
	for model, route := range before.ModelRouteOverrides {
		next := after.ModelRouteOverrides[model]
		if next.Default != renames[route.Default] {
			t.Fatalf("%s %s default %s -> %s", snap.Environment, model, route.Default, next.Default)
		}
		if len(next.Race) != len(route.Race) {
			t.Fatalf("%s %s race len", snap.Environment, model)
		}
		for i := range route.Race {
			if next.Race[i] != renames[route.Race[i]] {
				t.Fatalf("%s %s race[%d]=%s want %s", snap.Environment, model, i, next.Race[i], renames[route.Race[i]])
			}
		}
	}
}

func providerFromAudit(t *testing.T, item auditProvider, row auditRow) config.ProviderConfig {
	t.Helper()
	if len(item.Protocols) != 1 {
		t.Fatalf("%s has %d protocols", item.Instance, len(item.Protocols))
	}
	proto := item.Protocols[0]
	if proto.Protocol != row.Protocol {
		t.Fatalf("%s protocol snapshot=%s mapping=%s", item.Instance, proto.Protocol, row.Protocol)
	}
	if len(proto.Hosts) > 1 {
		t.Fatalf("%s has multiple host fields %v", item.Instance, proto.Hosts)
	}
	explicit := ""
	for _, host := range proto.Hosts {
		explicit = host
	}
	proxy := ""
	if proto.ProxyConfigured {
		proxy = "http://127.0.0.1:1081"
	}
	cfg := config.ProviderConfig{Models: append([]string(nil), item.Models...)}
	switch proto.Protocol {
	case "gemini":
		cfg.Gemini = &config.GeminiProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit), ProxyURL: proxy}
	case "vertexai":
		cfg.VertexAI = &config.VertexAIProviderConfig{Project: "test-project", ProxyURL: proxy}
	case "openai":
		cfg.OpenAI = &config.OpenAIProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit)}
	case "ark":
		cfg.Ark = &config.ArkProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit)}
	case "ark_video":
		cfg.ArkVideo = &config.ArkVideoProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit)}
	case "ltx":
		cfg.LTX = &config.LTXProviderConfig{BaseURL: urlFor(explicit)}
	case "dashscope_video":
		cfg.DashScopeVideo = &config.DashScopeVideoProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit)}
	case "ominilink_video":
		cfg.OminilinkVideo = &config.OminilinkVideoProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit)}
	case "gemini_video":
		cfg.GeminiVideo = &config.GeminiVideoProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit), ProxyURL: proxy}
	case "minimax_tts":
		cfg.MinimaxTTS = &config.MinimaxTTSProviderConfig{APIKey: "audit", Endpoint: urlFor(explicit)}
	case "elevenlabs_tts":
		cfg.ElevenLabsTTS = &config.ElevenLabsTTSProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit)}
	case "volcengine_asr":
		cfg.VolcengineASR = &config.VolcengineASRProviderConfig{APIKey: "audit", URL: urlFor(explicit)}
	case "aliyun_asr":
		cfg.AliyunASR = &config.AliyunASRProviderConfig{URL: urlFor(explicit)}
	case "fun_asr":
		cfg.FunASR = &config.FunASRProviderConfig{}
	case "qwen_asr":
		cfg.QwenASR = &config.QwenASRProviderConfig{}
	case "tencent_asr":
		cfg.TencentASR = &config.TencentASRProviderConfig{ProxyURL: proxy}
	case "assemblyai_asr":
		cfg.AssemblyAIASR = &config.AssemblyAIASRProviderConfig{URL: urlFor(explicit)}
	case "deepgram_asr":
		cfg.DeepgramASR = &config.DeepgramASRProviderConfig{URL: urlFor(explicit)}
	case "photoroom":
		cfg.Photoroom = &config.PhotoroomProviderConfig{APIKey: "audit", BaseURL: urlFor(explicit), ProxyURL: proxy}
	case "segment_person":
		cfg.SegmentPerson = &config.SegmentPersonProviderConfig{BaseURL: urlFor(explicit)}
	case "vworld_image":
		cfg.VWorldImage = &config.VWorldImageProviderConfig{BaseURL: urlFor(explicit)}
	case "human_yolo":
		cfg.HumanYOLO = &config.HumanYOLOProviderConfig{BaseURL: urlFor(explicit)}
	case "human_parser":
		cfg.HumanParser = &config.HumanParserProviderConfig{BaseURL: urlFor(explicit)}
	case "rekognition_detect":
		cfg.RekognitionDetect = &config.RekognitionDetectProviderConfig{Region: awsRegion(t, row.Host, "rekognition.")}
	case "rekognition_compare":
		cfg.RekognitionCompare = &config.RekognitionCompareProviderConfig{Region: awsRegion(t, row.Host, "rekognition.")}
	case "rekognition_faces":
		cfg.RekognitionFaces = &config.RekognitionFacesProviderConfig{Region: awsRegion(t, row.Host, "rekognition.")}
	case "rekognition_library":
		cfg.RekognitionLibrary = &config.RekognitionLibraryProviderConfig{Region: awsRegion(t, row.Host, "rekognition.")}
	case "facebody_compare":
		cfg.FacebodyCompare = &config.FacebodyCompareProviderConfig{Endpoint: explicit}
	case "facebody_detect":
		cfg.FacebodyDetect = &config.FacebodyDetectProviderConfig{Endpoint: explicit}
	case "facebody_library":
		cfg.FacebodyLibrary = &config.FacebodyLibraryProviderConfig{Endpoint: explicit}
	case "dashscope_embedding":
		cfg.DashScopeEmbedding = &config.DashScopeEmbeddingProviderConfig{BaseURL: urlFor(explicit)}
	case "dashscope_rerank":
		cfg.DashScopeRerank = &config.DashScopeRerankProviderConfig{BaseURL: urlFor(explicit)}
	case "cohere_embedding":
		cfg.CohereEmbedding = &config.CohereEmbeddingProviderConfig{BaseURL: urlFor(explicit)}
	case "cohere_rerank":
		cfg.CohereRerank = &config.CohereRerankProviderConfig{BaseURL: urlFor(explicit)}
	case "bedrock_rerank":
		cfg.BedrockRerank = &config.BedrockRerankProviderConfig{Region: awsRegion(t, row.Host, "bedrock-agent-runtime."), RoleARN: "arn:aws:iam::1:role/audit"}
	case "mixedbread_rerank":
		cfg.MixedbreadRerank = &config.MixedbreadRerankProviderConfig{BaseURL: urlFor(explicit)}
	case "image_seg":
		cfg.ImageSeg = &config.FacebodyCompareProviderConfig{Endpoint: explicit}
	case "fashion":
		cfg.Fashion = &config.FashionProviderConfig{BaseURL: urlFor(explicit)}
	default:
		t.Fatalf("unsupported audit protocol %s", proto.Protocol)
	}
	return cfg
}

func urlFor(host string) string {
	if host == "" {
		return ""
	}
	return "https://" + host + "/audit"
}

func awsRegion(t *testing.T, host, prefix string) string {
	t.Helper()
	if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, ".amazonaws.com") {
		t.Fatalf("host %s is not %s*.amazonaws.com", host, prefix)
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, prefix), ".amazonaws.com")
}

func loadSnapshot(t *testing.T, name string) auditSnapshot {
	t.Helper()
	var snap auditSnapshot
	decodeJSON(t, name, &snap)
	return snap
}

func loadAuditRows(t *testing.T) []auditRow {
	t.Helper()
	var rows []auditRow
	decodeJSON(t, "proposed-mapping.json", &rows)
	return rows
}

func decodeJSON(t *testing.T, name string, dest any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatal(err)
	}
}
