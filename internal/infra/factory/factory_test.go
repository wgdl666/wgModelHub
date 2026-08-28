package factory

import (
	"context"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/models"
)

func TestBuildOpenAIExposesImage(t *testing.T) {
	sets, err := Build(context.Background(), config.Config{
		Providers: map[string]config.ProviderConfig{
			"ominilink_gpt_image": {
				Models: []string{models.GPTImage2},
				OpenAI: &config.OpenAIProviderConfig{
					APIKey:  "k",
					BaseURL: "https://api.ominilink.ai/v1",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set := sets["ominilink_gpt_image"]
	if set.Text == nil || set.Image == nil {
		t.Fatalf("openai set=%#v", set)
	}
	if set.Video != nil {
		t.Fatalf("openai should not expose video")
	}
}

func TestBuildVideoProvidersExposeVideoOnly(t *testing.T) {
	sets, err := Build(context.Background(), config.Config{
		Providers: map[string]config.ProviderConfig{
			"dashscope_video": {
				Models: []string{models.Wan22I2VFlash, models.Wan27VideoEdit},
				DashScopeVideo: &config.DashScopeVideoProviderConfig{
					APIKey: "k",
				},
			},
			"ominilink_video": {
				Models: []string{models.KlingV3},
				OminilinkVideo: &config.OminilinkVideoProviderConfig{
					APIKey: "k",
				},
			},
			"gemini_video": {
				Models: []string{models.GeminiOmniFlashPreview},
				GeminiVideo: &config.GeminiVideoProviderConfig{
					APIKey: "k",
				},
			},
			"ark_video": {
				Models: []string{models.DoubaoSeedance25},
				ArkVideo: &config.ArkVideoProviderConfig{
					APIKey: "k",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, set := range sets {
		if set.Video == nil {
			t.Fatalf("%s missing video capability", name)
		}
		if set.Text != nil || set.Image != nil {
			t.Fatalf("%s should only expose video: %#v", name, set)
		}
	}
}

func TestBuildMinimaxTTSExposesSpeechOnly(t *testing.T) {
	sets, err := Build(context.Background(), config.Config{
		Providers: map[string]config.ProviderConfig{
			"minimax_tts": {
				Models: []string{models.Speech28Turbo},
				MinimaxTTS: &config.MinimaxTTSProviderConfig{
					APIKey:   "k",
					Endpoint: "wss://example.invalid/ws",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set := sets["minimax_tts"]
	if set.Speech == nil {
		t.Fatal("missing speech capability")
	}
	if set.Text != nil || set.Image != nil || set.Video != nil {
		t.Fatalf("tts should only expose speech: %#v", set)
	}
}

func TestBuildVideoProvidersAcceptZeroPollConfig(t *testing.T) {
	_, err := Build(context.Background(), config.Config{
		Providers: map[string]config.ProviderConfig{
			"dashscope_video": {
				Models:         []string{models.Wan22I2VFlash},
				DashScopeVideo: &config.DashScopeVideoProviderConfig{APIKey: "k"},
			},
			"ominilink_video": {
				Models:         []string{models.KlingV3},
				OminilinkVideo: &config.OminilinkVideoProviderConfig{APIKey: "k"},
			},
			"gemini_video": {
				Models:      []string{models.GeminiOmniFlashPreview},
				GeminiVideo: &config.GeminiVideoProviderConfig{APIKey: "k"},
			},
			"ark_video": {
				Models:   []string{models.DoubaoSeedance25},
				ArkVideo: &config.ArkVideoProviderConfig{APIKey: "k"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}
