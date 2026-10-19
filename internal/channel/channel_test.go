package channel

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/models"
	"gopkg.in/yaml.v3"
)

func TestHostUsesExplicitBaseURLNotProxy(t *testing.T) {
	id, err := Of(config.ProviderConfig{
		Gemini: &config.GeminiProviderConfig{
			BaseURL:  "https://api.aig-ai.com/v1",
			ProxyURL: "http://127.0.0.1:1081",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id.Host != "api.aig-ai.com" || id.Channel != "AIG" || id.Protocol != "gemini" {
		t.Fatalf("identity=%+v", id)
	}
}

func TestGeminiAndVertexDefaultsIgnoreProxy(t *testing.T) {
	gemini, err := Of(config.ProviderConfig{
		Gemini: &config.GeminiProviderConfig{ProxyURL: "http://proxy.internal:1081"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gemini.Host != geminiDefaultHost || gemini.Channel != "Gemini Developer API" {
		t.Fatalf("gemini=%+v", gemini)
	}
	vertex, err := Of(config.ProviderConfig{
		VertexAI: &config.VertexAIProviderConfig{Project: "123", ProxyURL: "http://proxy.internal:1081"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if vertex.Host != vertexDefaultHost || vertex.Channel != "Vertex AI" {
		t.Fatalf("vertex=%+v", vertex)
	}
}

func TestRegionChoosesAWSHostNotDeployment(t *testing.T) {
	sg, err := Of(config.ProviderConfig{RekognitionDetect: &config.RekognitionDetectProviderConfig{Region: "ap-southeast-1"}})
	if err != nil {
		t.Fatal(err)
	}
	ohio, err := Of(config.ProviderConfig{RekognitionDetect: &config.RekognitionDetectProviderConfig{Region: "us-east-2"}})
	if err != nil {
		t.Fatal(err)
	}
	if sg.Host != "rekognition.ap-southeast-1.amazonaws.com" || sg.Channel != "AWS Rekognition_ap-southeast-1" {
		t.Fatalf("sg=%+v", sg)
	}
	if ohio.Host != "rekognition.us-east-2.amazonaws.com" || ohio.Channel != "AWS Rekognition_us-east-2" {
		t.Fatalf("ohio=%+v", ohio)
	}
	rerank, err := Of(config.ProviderConfig{BedrockRerank: &config.BedrockRerankProviderConfig{
		Region:  "us-east-1",
		RoleARN: "arn:aws:iam::054043816891:role/example",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if rerank.Host != "bedrock-agent-runtime.us-east-1.amazonaws.com" || strings.Contains(rerank.Host, "sts.") {
		t.Fatalf("rerank host must stay on agent runtime, got %+v", rerank)
	}
	embed, err := Of(config.ProviderConfig{BedrockEmbedding: &config.BedrockEmbeddingProviderConfig{Region: "us-east-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if embed.Host != "bedrock-runtime.us-east-1.amazonaws.com" {
		t.Fatalf("embed=%+v", embed)
	}
}

func TestWorkspaceOverridesDashScopeDefault(t *testing.T) {
	plain, err := Of(config.ProviderConfig{FunASR: &config.FunASRProviderConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Host != dashscopeDefaultHost || plain.Channel != "阿里云百炼" {
		t.Fatalf("plain=%+v", plain)
	}
	workspace, err := Of(config.ProviderConfig{QwenASR: &config.QwenASRProviderConfig{WorkspaceID: "ws-example"}})
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Host != "ws-example.cn-beijing.maas.aliyuncs.com" || workspace.Channel != "阿里云 MaaS_ws-example" {
		t.Fatalf("workspace=%+v", workspace)
	}
	explicit, err := Of(config.ProviderConfig{FunASR: &config.FunASRProviderConfig{
		WorkspaceID: "ws-example",
		URL:         "wss://dashscope.aliyuncs.com/api-ws/v1/inference",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Host != dashscopeDefaultHost {
		t.Fatalf("explicit url must win over workspace, got %+v", explicit)
	}
}

func TestWeiguangExceptionKeepsHost(t *testing.T) {
	segment, err := Of(config.ProviderConfig{
		Models:        []string{models.SegmentPersonBria, models.SegmentSubjectBria},
		SegmentPerson: &config.SegmentPersonProviderConfig{BaseURL: "https://segment-person.model.wgdl.tech"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if segment.Channel != channelWeiguang || segment.Host != "segment-person.model.wgdl.tech" {
		t.Fatalf("segment=%+v", segment)
	}
	legacy, err := Of(config.ProviderConfig{
		Models: []string{"ltx"},
		LTX:    &config.LTXProviderConfig{BaseURL: "https://u847021-9507-702f766a.bjb2.seetacloud.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Channel != channelWeiguang || legacy.Host != "u847021-9507-702f766a.bjb2.seetacloud.com" {
		t.Fatalf("legacy ltx=%+v", legacy)
	}
	parser, err := Of(config.ProviderConfig{
		Models:      []string{models.HumanParse},
		HumanParser: &config.HumanParserProviderConfig{BaseURL: "http://172.31.42.52:6105"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if parser.Channel != "172.31.42.52" || parser.Host != "172.31.42.52" {
		t.Fatalf("human parser must stay on its host, got %+v", parser)
	}
}

func TestDownloadHostIsNotOminilinkSubmitChannel(t *testing.T) {
	submit, err := Of(config.ProviderConfig{OminilinkVideo: &config.OminilinkVideoProviderConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	if submit.Host != ominilinkVideoHost || submit.Channel != "AIG VG" {
		t.Fatalf("submit=%+v", submit)
	}
	download, err := Of(config.ProviderConfig{OminilinkVideo: &config.OminilinkVideoProviderConfig{
		BaseURL: "https://download-vod.aig-ai.com/video",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if download.Host != "download-vod.aig-ai.com" || download.Channel == "AIG" || download.Channel == "AIG VG" {
		t.Fatalf("download CDN must stay its own host, got %+v", download)
	}
}

func TestNamesKeepVLRoleWithoutSiblingAndAreIdempotent(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"dashscope_vl": {
			Models: []string{models.Qwen3VLPlus},
			OpenAI: &config.OpenAIProviderConfig{BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"},
		},
		"dashscope_video": {
			DashScopeVideo: &config.DashScopeVideoProviderConfig{BaseURL: "https://dashscope.aliyuncs.com/api/v1"},
		},
	}
	names, err := Names(providers)
	if err != nil {
		t.Fatal(err)
	}
	if names["dashscope_vl"] != "阿里云百炼_openai_vl" || names["dashscope_video"] != "阿里云百炼_dashscope_video" {
		t.Fatalf("names=%v", names)
	}
	renamed := map[string]config.ProviderConfig{
		names["dashscope_vl"]:    providers["dashscope_vl"],
		names["dashscope_video"]: providers["dashscope_video"],
	}
	again, err := Names(renamed)
	if err != nil {
		t.Fatal(err)
	}
	for name, next := range again {
		if name != next {
			t.Fatalf("second pass changed %s to %s", name, next)
		}
	}
}

func TestWeiguangInstanceNames(t *testing.T) {
	providers := map[string]config.ProviderConfig{
		"薇光点亮": {
			Models:        []string{models.SegmentPersonBria},
			SegmentPerson: &config.SegmentPersonProviderConfig{BaseURL: "https://segment-person.model.wgdl.tech"},
		},
		"薇光点亮_image": {
			Models:      []string{models.VWorldOutfit10},
			VWorldImage: &config.VWorldImageProviderConfig{BaseURL: "https://vmind.example"},
		},
		"薇光点亮_video": {
			Models: []string{models.VWorldOutfitVideo10},
			LTX:    &config.LTXProviderConfig{BaseURL: "https://ltx.example"},
		},
	}
	names, err := Names(providers)
	if err != nil {
		t.Fatal(err)
	}
	if names["薇光点亮"] != "wgdl_segment_person" || names["薇光点亮_image"] != "wgdl_image" || names["薇光点亮_video"] != "wgdl_video" {
		t.Fatalf("names=%v", names)
	}
}

func TestRewritePreservesUnknownFieldsCredentialsAndRaceOrder(t *testing.T) {
	const original = `
note: keep-me
providers:
  dashscope_chat:
    models: [qwen-flash]
    custom_flag: 1
    openai:
      api_key: secret-chat
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
  dashscope_vl:
    models: [qwen3-vl-plus]
    openai:
      api_key: secret-vl
      base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
  gemini_api:
    models: [gemini-2.5-flash]
    gemini:
      api_key: secret-gemini
      proxy_url: http://127.0.0.1:1081
model_routes:
  qwen-flash: dashscope_chat
  gemini-2.5-flash:
    default: gemini_api
    race: [dashscope_vl, gemini_api]
`
	out, renames, err := RewriteYAML([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "dashscope_chat:") || strings.Contains(string(out), "secret") && !strings.Contains(string(out), "secret-chat") {
		t.Fatalf("output lost or leaked unexpectedly:\n%s", out)
	}
	var decoded struct {
		Note      string `yaml:"note"`
		Providers map[string]struct {
			Models     []string `yaml:"models"`
			CustomFlag int      `yaml:"custom_flag"`
			OpenAI     *struct {
				APIKey  string `yaml:"api_key"`
				BaseURL string `yaml:"base_url"`
			} `yaml:"openai"`
			Gemini *struct {
				APIKey   string `yaml:"api_key"`
				ProxyURL string `yaml:"proxy_url"`
			} `yaml:"gemini"`
		} `yaml:"providers"`
		Routes map[string]yaml.Node `yaml:"model_routes"`
	}
	if err := yaml.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Note != "keep-me" {
		t.Fatalf("note=%q", decoded.Note)
	}
	chat := decoded.Providers[renames["dashscope_chat"]]
	if chat.CustomFlag != 1 || chat.OpenAI == nil || chat.OpenAI.APIKey != "secret-chat" || chat.Models[0] != "qwen-flash" {
		t.Fatalf("chat provider=%+v rename=%v", chat, renames)
	}
	vl := decoded.Providers[renames["dashscope_vl"]]
	if vl.OpenAI == nil || vl.OpenAI.APIKey != "secret-vl" {
		t.Fatalf("vl=%+v", vl)
	}
	gemini := decoded.Providers[renames["gemini_api"]]
	if gemini.Gemini == nil || gemini.Gemini.APIKey != "secret-gemini" || gemini.Gemini.ProxyURL != "http://127.0.0.1:1081" {
		t.Fatalf("gemini=%+v", gemini)
	}
	var routes map[string]any
	if err := yaml.Unmarshal(out, &struct {
		Routes *map[string]any `yaml:"model_routes"`
	}{&routes}); err != nil {
		t.Fatal(err)
	}
	// re-decode routes with the config type so race order is visible
	var cfg config.Config
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ModelRouteOverrides["qwen-flash"].Default != renames["dashscope_chat"] {
		t.Fatalf("string route=%+v", cfg.ModelRouteOverrides["qwen-flash"])
	}
	race := cfg.ModelRouteOverrides["gemini-2.5-flash"]
	if race.Default != renames["gemini_api"] || len(race.Race) != 2 || race.Race[0] != renames["dashscope_vl"] || race.Race[1] != renames["gemini_api"] {
		t.Fatalf("race=%+v", race)
	}
	again, _, err := RewriteYAML(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(out) {
		t.Fatalf("second rewrite changed YAML")
	}
}

func TestSameProtocolWithoutFamilyUsesHost(t *testing.T) {
	// 协议相同、模型不属于四类族时，渠道和实例键都按主机，不进薇光点亮。
	cases := []config.ProviderConfig{
		{
			Models:        []string{"custom-segment-worker"},
			SegmentPerson: &config.SegmentPersonProviderConfig{BaseURL: "https://other-segment.example/api"},
		},
		{
			VWorldImage: &config.VWorldImageProviderConfig{BaseURL: "https://other-image.example/v1"},
		},
		{
			Models: []string{"custom-video"},
			LTX:    &config.LTXProviderConfig{BaseURL: "https://other-video.example/vton"},
		},
	}
	hosts := []string{"other-segment.example", "other-image.example", "other-video.example"}
	for i, cfg := range cases {
		id, err := Of(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if id.Channel != hosts[i] || id.Host != hosts[i] {
			t.Fatalf("identity=%+v", id)
		}
		names, err := Names(map[string]config.ProviderConfig{"old": cfg})
		if err != nil {
			t.Fatal(err)
		}
		if names["old"] != hosts[i] {
			t.Fatalf("names=%v", names)
		}
	}
}

func TestNewFamilyIDsUseWeiguangException(t *testing.T) {
	// 同族新 ID 不在 models 常量里，仍按例外归薇光，并按协议落到对应实例键。
	person := config.ProviderConfig{
		Models:        []string{"segment-person-next"},
		SegmentPerson: &config.SegmentPersonProviderConfig{BaseURL: "https://seg.example/api"},
	}
	subject := config.ProviderConfig{
		Models:        []string{"segment-subject-next"},
		SegmentPerson: &config.SegmentPersonProviderConfig{BaseURL: "https://sub.example/api"},
	}
	image := config.ProviderConfig{
		Models:      []string{"VWorld_studio_2.0"},
		VWorldImage: &config.VWorldImageProviderConfig{BaseURL: "https://img.example/v1"},
	}
	video := config.ProviderConfig{
		Models: []string{"VWorld_outfit_video_2.0"},
		LTX:    &config.LTXProviderConfig{BaseURL: "https://vid.example/vton"},
	}
	for _, cfg := range []config.ProviderConfig{person, subject, image, video} {
		id, err := Of(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if id.Channel != channelWeiguang {
			t.Fatalf("identity=%+v", id)
		}
	}
	assertName := func(cfg config.ProviderConfig, want string) {
		t.Helper()
		names, err := Names(map[string]config.ProviderConfig{"old": cfg})
		if err != nil {
			t.Fatal(err)
		}
		if names["old"] != want {
			t.Fatalf("names=%v want %s", names, want)
		}
	}
	assertName(person, instanceSegmentPerson)
	assertName(subject, instanceSegmentPerson)
	assertName(image, instanceWeiguangImage)
	assertName(video, instanceWeiguangVideo)
}

func TestRewriteRejectsNameCollision(t *testing.T) {
	// 两份 ltx 都会落到 wgdl_video。渠道可以跨主机合并，实例键不能撞车。
	const original = `
providers:
  a:
    models: [ltx]
    ltx:
      base_url: https://a.example
      duration: 1
      fps: 1
      poll_interval: 1
  b:
    models: [VWorld_outfit_video_1.0]
    ltx:
      base_url: https://b.example
      duration: 1
      fps: 1
      poll_interval: 1
`
	if _, _, err := RewriteYAML([]byte(original)); err == nil {
		t.Fatal("two weiguang video instances must be rejected")
	}
}

func TestGoogleRerankChannelUsesRankingHost(t *testing.T) {
	id, err := Of(config.ProviderConfig{GoogleRerank: &config.GoogleRerankProviderConfig{Project: "test-project", ProxyURL: "http://proxy.internal:1081"}})
	if err != nil {
		t.Fatal(err)
	}
	if id.Host != "discoveryengine.googleapis.com" || id.Channel != "Vertex AI" || id.Protocol != "google_rerank" {
		t.Fatalf("identity=%+v", id)
	}
}
