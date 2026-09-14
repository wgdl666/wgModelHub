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
	want := []string{models.Gemini37Flash, models.Qwen3VLPlus, models.Qwen37Flash, models.GPTImage2, models.LTX, models.Speech28Turbo}
	if !sameStrings(got, want) {
		t.Fatalf("models=%v want=%v", got, want)
	}
	if contains(got, "not-in-catalog") || contains(got, models.Qwen38Flash) {
		t.Fatalf("uncatalogued or unrouted id leaked: %v", got)
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
	want := []string{models.Gemini37Flash, models.Qwen37Flash}
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
