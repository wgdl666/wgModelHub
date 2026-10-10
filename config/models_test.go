package config

import (
	"os"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
	"gopkg.in/yaml.v3"
)

func TestDockerContextIncludesExampleYAMLForBuilderTests(t *testing.T) {
	raw, err := os.ReadFile("../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\n!config/example.modelHub.yaml\n") {
		t.Fatal(".dockerignore must re-include config/example.modelHub.yaml for Docker builder tests")
	}
}

func TestDockerContextIncludesGPTImage2WrapperForBuilderTests(t *testing.T) {
	raw, err := os.ReadFile("../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	const rules = "\nscripts/*\n!scripts/examples/\nscripts/examples/*\n!scripts/examples/gpt-image-2.sh\n"
	if !strings.Contains(string(raw), rules) {
		t.Fatal(".dockerignore must include only the gpt-image-2 wrapper from scripts for Docker builder tests")
	}
}

func TestDockerfileDefaultsToProductionStage(t *testing.T) {
	raw, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "FROM runtime AS production" {
		t.Fatalf("Dockerfile final stage=%q, want production", got)
	}
}

func TestExampleYAMLUsesExactlyKnownModelIDs(t *testing.T) {
	raw, err := os.ReadFile("example.modelHub.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]struct {
			Models []string `yaml:"models"`
			Ark    *struct {
				EndpointID string `yaml:"endpoint_id"`
			} `yaml:"ark"`
			OpenAI *struct {
				BaseURL string `yaml:"base_url"`
			} `yaml:"openai"`
			VertexAI *struct {
				APIKey  string `yaml:"api_key"`
				Project string `yaml:"project"`
			} `yaml:"vertexai"`
			VWorldImage *struct {
				BaseURL  string `yaml:"base_url"`
				Username string `yaml:"username"`
				Password string `yaml:"password"`
			} `yaml:"vworld_image"`
		} `yaml:"providers"`
		ModelRoutes map[string]string `yaml:"model_routes"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	// 允许双渠道声明同一真实模型，但必须有 model_routes 显式选定，且目标实例确实声明了该模型。
	declaredBy := map[string]map[string]struct{}{}
	fromYAML := map[string]struct{}{}
	for name, provider := range parsed.Providers {
		for _, model := range provider.Models {
			fromYAML[model] = struct{}{}
			if declaredBy[model] == nil {
				declaredBy[model] = map[string]struct{}{}
			}
			declaredBy[model][name] = struct{}{}
		}
	}
	for model, providers := range declaredBy {
		if len(providers) == 1 {
			continue
		}
		selected, ok := parsed.ModelRoutes[model]
		if !ok {
			t.Fatalf("example yaml multi-binds %s without model_routes", model)
		}
		if _, ok := providers[selected]; !ok {
			t.Fatalf("example model_routes[%s]=%s is not among declaring providers", model, selected)
		}
	}
	known := map[string]struct{}{}
	for _, model := range models.All() {
		known[model] = struct{}{}
		if _, ok := fromYAML[model]; !ok {
			t.Fatalf("known model %s is missing from example.modelHub.yaml", model)
		}
	}
	for model := range fromYAML {
		if _, ok := known[model]; !ok {
			t.Fatalf("example.modelHub.yaml has undocumented model %s", model)
		}
	}

	// 2.1 Pro 在示例配置中绑定独立 Ark endpoint，供衣橱候选搭配链路路由。
	provider, ok := parsed.Providers["ark_doubao_21_pro"]
	if !ok {
		t.Fatal("missing provider ark_doubao_21_pro")
	}
	if len(provider.Models) != 1 || provider.Models[0] != models.DoubaoSeed21Pro {
		t.Fatalf("ark_doubao_21_pro models=%v, want [%q]", provider.Models, models.DoubaoSeed21Pro)
	}
	if provider.Ark == nil {
		t.Fatal("ark_doubao_21_pro ark.endpoint_id is missing")
	}
	if provider.Ark.EndpointID != "ep-20260902131944-rj4cb" {
		t.Fatalf("ark_doubao_21_pro endpoint_id=%q, want ep-20260902131944-rj4cb", provider.Ark.EndpointID)
	}

	// DeepSeek-V4-Flash 正式版绑定独立 Ark endpoint，禁止与 Doubao 或其他 DeepSeek ID 共用部署。
	deepseek, ok := parsed.Providers["ark_deepseek_v4_flash"]
	if !ok {
		t.Fatal("missing provider ark_deepseek_v4_flash")
	}
	if len(deepseek.Models) != 1 || deepseek.Models[0] != models.DeepSeekV4Flash {
		t.Fatalf("ark_deepseek_v4_flash models=%v, want [%q]", deepseek.Models, models.DeepSeekV4Flash)
	}
	if deepseek.Ark == nil {
		t.Fatal("ark_deepseek_v4_flash ark.endpoint_id is missing")
	}
	if deepseek.Ark.EndpointID != "ep-20260904161804-km74x" {
		t.Fatalf("ark_deepseek_v4_flash endpoint_id=%q, want ep-20260904161804-km74x", deepseek.Ark.EndpointID)
	}

	// V4.1 是新增独立 provider，不是切换 V4：各自只声明各自模型，并绑定不同 endpoint。
	deepseek41, ok := parsed.Providers["ark_deepseek_v41_flash"]
	if !ok {
		t.Fatal("missing provider ark_deepseek_v41_flash")
	}
	if len(deepseek41.Models) != 1 || deepseek41.Models[0] != models.DeepSeekV41Flash {
		t.Fatalf("ark_deepseek_v41_flash models=%v, want [%q]", deepseek41.Models, models.DeepSeekV41Flash)
	}
	if deepseek41.Ark == nil {
		t.Fatal("ark_deepseek_v41_flash ark.endpoint_id is missing")
	}
	if deepseek41.Ark.EndpointID != "ep-20260916172350-hv45h" {
		t.Fatalf("ark_deepseek_v41_flash endpoint_id=%q, want ep-20260916172350-hv45h", deepseek41.Ark.EndpointID)
	}
	if deepseek.Ark.EndpointID == deepseek41.Ark.EndpointID {
		t.Fatal("V4 and V4.1 must bind distinct Ark endpoints")
	}

	// 智谱 GLM 必须绑独立 OpenAI-compatible 实例，base_url 拼上 /chat/completions 后等于官方 PaaS 路径。
	glm, ok := parsed.Providers["zhipu_glm"]
	if !ok {
		t.Fatal("missing provider zhipu_glm")
	}
	if len(glm.Models) != 1 || glm.Models[0] != models.GLM53Flash {
		t.Fatalf("zhipu_glm models=%v, want [%q]", glm.Models, models.GLM53Flash)
	}
	if glm.OpenAI == nil || glm.OpenAI.BaseURL != "https://open.bigmodel.cn/api/paas/v4" {
		t.Fatalf("zhipu_glm openai.base_url=%v, want https://open.bigmodel.cn/api/paas/v4", glm.OpenAI)
	}

	// FLUX.2 必须绑独立 OpenAI Images 实例，不能并入 async_gpt_image；当前部署只接受 i2i。
	flux2, ok := parsed.Providers["flux2_klein_image"]
	if !ok {
		t.Fatal("missing provider flux2_klein_image")
	}
	if len(flux2.Models) != 1 || flux2.Models[0] != models.Flux2Klein9B {
		t.Fatalf("flux2_klein_image models=%v, want [%q]", flux2.Models, models.Flux2Klein9B)
	}
	if flux2.OpenAI == nil || flux2.OpenAI.BaseURL != "https://uu847021-9507-702f766a.bjb2.seetacloud.com:8443/flux2/v1" {
		t.Fatalf("flux2_klein_image openai.base_url=%v, want SeeTacloud flux2/v1", flux2.OpenAI)
	}

	// 薇光点亮公网 FLUX 必须独立实例；密码只允许环境变量占位，禁止写内网调试口。
	vworld, ok := parsed.Providers["vworld_flux_image"]
	if !ok {
		t.Fatal("missing provider vworld_flux_image")
	}
	if len(vworld.Models) != 2 || vworld.Models[0] != models.VWorldWardrobe10 || vworld.Models[1] != models.VWorldOutfit10 {
		t.Fatalf("vworld_flux_image models=%v", vworld.Models)
	}
	if vworld.VWorldImage == nil ||
		vworld.VWorldImage.BaseURL != "https://vmind-image.model.wgdl.tech" ||
		vworld.VWorldImage.Username != "wgdl" ||
		vworld.VWorldImage.Password != "${VWORLD_IMAGE_PASSWORD}" {
		t.Fatalf("vworld_flux_image config=%#v", vworld.VWorldImage)
	}
	if strings.Contains(vworld.VWorldImage.BaseURL, "10.200.") || strings.Contains(vworld.VWorldImage.BaseURL, "127.0.0.1") {
		t.Fatal("vworld_flux_image must not use intranet/localhost upstream")
	}

	// GPT Image 2 / 2.5 复用现网已实测的 AIG OpenAI-compatible Images 实例；不能并入 Gemini 生图。
	asyncGPT, ok := parsed.Providers["async_gpt_image"]
	if !ok {
		t.Fatal("missing provider async_gpt_image")
	}
	wantGPTModels := []string{models.GPTImage2, models.GPTImage25Flare, models.GPTImage25Sunburst}
	if len(asyncGPT.Models) != 3 || asyncGPT.Models[0] != wantGPTModels[0] || asyncGPT.Models[1] != wantGPTModels[1] || asyncGPT.Models[2] != wantGPTModels[2] {
		t.Fatalf("async_gpt_image models=%v, want %v", asyncGPT.Models, wantGPTModels)
	}
	if asyncGPT.OpenAI == nil || asyncGPT.OpenAI.BaseURL != "https://api.aig-ai.com/v1" {
		t.Fatalf("async_gpt_image openai.base_url=%v, want https://api.aig-ai.com/v1", asyncGPT.OpenAI)
	}

	// 示例把 Nano Banana 2.1 放在独立 Vertex 实例；单声明不写 model_routes。
	vertexChat, ok := parsed.Providers["vertex_chat"]
	if !ok {
		t.Fatal("missing provider vertex_chat")
	}
	if len(vertexChat.Models) != 1 || vertexChat.Models[0] != models.Gemini20Flash001 {
		t.Fatalf("vertex_chat models=%v, want [%q]", vertexChat.Models, models.Gemini20Flash001)
	}
	nano, ok := parsed.Providers["vertex_nano_banana"]
	if !ok {
		t.Fatal("missing provider vertex_nano_banana")
	}
	if len(nano.Models) != 1 || nano.Models[0] != models.GeminiNanoBanana21 {
		t.Fatalf("vertex_nano_banana models=%v, want [%q]", nano.Models, models.GeminiNanoBanana21)
	}
	if nano.VertexAI == nil || nano.VertexAI.APIKey != "${VERTEX_API_KEY}" || nano.VertexAI.Project != "${VERTEX_PROJECT}" {
		t.Fatalf("vertex_nano_banana vertexai=%#v", nano.VertexAI)
	}
	if vertexChat.VertexAI == nil || vertexChat.VertexAI.Project != "" {
		t.Fatalf("vertex_chat must stay key-only, vertexai=%#v", vertexChat.VertexAI)
	}
	if _, routed := parsed.ModelRoutes[models.GeminiNanoBanana21]; routed {
		t.Fatal("single-declared nano banana must not use model_routes")
	}
	if parsed.ModelRoutes[models.Gemini25Flash] != "" {
		t.Fatalf("gemini-2.5-flash model_routes=%q", parsed.ModelRoutes[models.Gemini25Flash])
	}

	// 四候选：Gemini 两个进 gemini_main；Qwen3.8 进 hub_chat；Claude 用独立 OpenAI-compat 指官方 Anthropic endpoint。
	gemini, ok := parsed.Providers["gemini_main"]
	if !ok {
		t.Fatal("missing provider gemini_main")
	}
	for _, want := range []string{models.Gemini38Flash, models.Gemini35FlashLite} {
		found := false
		for _, m := range gemini.Models {
			if m == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("gemini_main missing %q in %v", want, gemini.Models)
		}
	}
	hubChat, ok := parsed.Providers["hub_chat"]
	if !ok {
		t.Fatal("missing provider hub_chat")
	}
	foundQwen38 := false
	for _, m := range hubChat.Models {
		if m == models.Qwen38Flash {
			foundQwen38 = true
			break
		}
	}
	if !foundQwen38 {
		t.Fatalf("hub_chat missing %q in %v", models.Qwen38Flash, hubChat.Models)
	}
	claude, ok := parsed.Providers["anthropic_claude"]
	if !ok {
		t.Fatal("missing provider anthropic_claude")
	}
	if len(claude.Models) != 1 || claude.Models[0] != models.ClaudeHaiku45 {
		t.Fatalf("anthropic_claude models=%v, want [%q]", claude.Models, models.ClaudeHaiku45)
	}
	if claude.OpenAI == nil || claude.OpenAI.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("anthropic_claude openai.base_url=%v, want https://api.anthropic.com/v1", claude.OpenAI)
	}
}
