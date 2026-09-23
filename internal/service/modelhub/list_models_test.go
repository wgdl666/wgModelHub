package modelhub

import (
	"context"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func listModelsTestService() *Service {
	return newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"hub_chat": {
				Models: []string{models.Qwen37Flash, models.Gemini37Flash},
				OpenAI: &config.OpenAIProviderConfig{APIKey: "k"},
			},
			// V4.1 已编目且路由存在时必须进入 ListModels；与 V4 分 provider，禁止靠 alias 冒充。
			"ark_deepseek_v41_flash": {
				Models: []string{models.DeepSeekV41Flash},
				Ark:    &config.ArkProviderConfig{APIKey: "k", EndpointID: "ep-20260916172350-hv45h"},
			},
			"vision": {
				Models: []string{models.Qwen3VLPlus},
				OpenAI: &config.OpenAIProviderConfig{APIKey: "k"},
			},
			"image": {
				Models: []string{models.GPTImage2},
				OpenAI: &config.OpenAIProviderConfig{APIKey: "k"},
			},
			"video": {
				Models: []string{models.LTX},
				LTX:    &config.LTXProviderConfig{BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1},
			},
			"tts": {
				Models:     []string{models.Speech28Turbo},
				MinimaxTTS: &config.MinimaxTTSProviderConfig{APIKey: "k"},
			},
			"embed": {
				Models:             []string{models.Qwen3VLEmbedding},
				DashScopeEmbedding: &config.DashScopeEmbeddingProviderConfig{BaseURL: "https://example.invalid", APIKey: "k"},
			},
			"rerank": {
				Models:          []string{models.Qwen37TextRerank},
				DashScopeRerank: &config.DashScopeRerankProviderConfig{BaseURL: "https://example.invalid", APIKey: "k"},
			},
			"face_compare": {
				Models:          []string{models.FacebodyCompareFace},
				FacebodyCompare: &config.FacebodyCompareProviderConfig{Endpoint: "facebody.example.com", AccessKeyID: "k", AccessKeySecret: "s"},
			},
			"face_detect": {
				Models:         []string{models.FacebodyDetectFace},
				FacebodyDetect: &config.FacebodyDetectProviderConfig{Endpoint: "facebody.example.com", AccessKeyID: "k", AccessKeySecret: "s"},
			},
			"face_library": {
				Models:          []string{models.FacebodyFaceLibrary},
				FacebodyLibrary: &config.FacebodyLibraryProviderConfig{Endpoint: "facebody.example.com", AccessKeyID: "k", AccessKeySecret: "s", Database: "wgdl_dev"},
			},
			"person_detect": {
				Models:    []string{models.HumanYOLO},
				HumanYOLO: &config.HumanYOLOProviderConfig{BaseURL: "https://example.invalid", Username: "u", Password: "p"},
			},
			"human_parser": {
				Models:      []string{models.HumanParser},
				HumanParser: &config.HumanParserProviderConfig{BaseURL: "https://example.invalid"},
			},
			"extra": {
				Models: []string{"not-in-catalog"},
				Ark:    &config.ArkProviderConfig{APIKey: "k"},
			},
		},
	}, nil, nil)
}

func TestListModelsReturnsRoutedPublicCategories(t *testing.T) {
	resp, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := listModelIDs(resp)
	want := []string{models.Gemini37Flash, models.DeepSeekV41Flash, models.Qwen3VLPlus, models.Qwen37Flash, models.GPTImage2, models.LTX, models.Speech28Turbo, models.Qwen3VLEmbedding, models.Qwen37TextRerank, models.FacebodyCompareFace, models.FacebodyDetectFace, models.FacebodyFaceLibrary, models.HumanYOLO, models.HumanParser}
	if !sameStrings(got, want) {
		t.Fatalf("models=%v want=%v", got, want)
	}
	if contains(got, "not-in-catalog") || contains(got, models.Qwen38Flash) || contains(got, models.DeepSeekV4Flash) {
		t.Fatalf("unknown category or unrouted id leaked: %v", got)
	}
}

func TestListModelsFiltersLLM(t *testing.T) {
	resp, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_LLM,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := listModelIDs(resp)
	want := []string{models.DeepSeekV41Flash}
	if !sameStrings(got, want) {
		t.Fatalf("llm=%v want=%v", got, want)
	}
	for _, item := range resp.GetModels() {
		if item.GetCategory() != modelhubv2.ModelCategory_MODEL_CATEGORY_LLM {
			t.Fatalf("category=%v model=%s", item.GetCategory(), item.GetModel())
		}
	}
}

func TestListModelsFiltersSpeech(t *testing.T) {
	resp, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_SPEECH,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := listModelIDs(resp)
	if !sameStrings(got, []string{models.Speech28Turbo}) {
		t.Fatalf("speech=%v", got)
	}
	if resp.GetModels()[0].GetCategory() != modelhubv2.ModelCategory_MODEL_CATEGORY_SPEECH {
		t.Fatalf("category=%v", resp.GetModels()[0].GetCategory())
	}
}

func TestListModelsFiltersEmbeddingApartFromMultimodal(t *testing.T) {
	resp, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_EMBEDDING,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(listModelIDs(resp), []string{models.Qwen3VLEmbedding}) {
		t.Fatalf("embedding=%v", listModelIDs(resp))
	}
	if resp.GetModels()[0].GetCategory() != modelhubv2.ModelCategory_MODEL_CATEGORY_EMBEDDING {
		t.Fatalf("category=%v", resp.GetModels()[0].GetCategory())
	}
	multi, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_MULTIMODAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(listModelIDs(multi), []string{models.Gemini37Flash, models.Qwen3VLPlus, models.Qwen37Flash}) {
		t.Fatalf("multimodal=%v", listModelIDs(multi))
	}
	person, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_PERSON_DETECT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(listModelIDs(person), []string{models.HumanYOLO}) {
		t.Fatalf("person_detect=%v", listModelIDs(person))
	}
	parser, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_HUMAN_PARSER,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(listModelIDs(parser), []string{models.HumanParser}) {
		t.Fatalf("human_parser=%v", listModelIDs(parser))
	}
}

func TestListModelsRejectsUnknownCategory(t *testing.T) {
	_, err := listModelsTestService().ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory(99),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func listModelIDs(resp *modelhubv2.ListModelsResponse) []string {
	out := make([]string, 0, len(resp.GetModels()))
	for _, item := range resp.GetModels() {
		out = append(out, item.GetModel())
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(want))
	for _, value := range want {
		seen[value]++
	}
	for _, value := range got {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}
