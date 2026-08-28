package config

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func validConfig() Config {
	return Config{
		Server: struct {
			ListenAddress       string
			PublicListenAddress string
		}{ListenAddress: ":50053"},
		Logfire: LogfireConfig{
			Token:   "logfire-token",
			Env:     "production",
			Service: "wg-model-hub",
		},
		Database: DatabaseConfig{DSN: "postgres://modelhub:modelhub@127.0.0.1:5432/modelhub?sslmode=disable"},
		Providers: map[string]ProviderConfig{
			"ark": {
				Models: []string{"doubao-chat"},
				Ark:    &ArkProviderConfig{APIKey: "key"},
			},
			"gemini": {
				Models: []string{models.Gemini25Flash, models.Gemini25FlashImage},
				Gemini: &GeminiProviderConfig{APIKey: "key"},
			},
			"ltx": {
				Models: []string{models.LTX},
				LTX: &LTXProviderConfig{
					BaseURL:      "https://ltx.example",
					Token:        "token",
					Duration:     4,
					FPS:          24,
					PollInterval: 2,
					MaxPollTime:  120,
				},
			},
		},
	}
}

func TestValidateAcceptsUniqueModels(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsDuplicateModelsWithoutRoute(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["other"] = ProviderConfig{
		Models: []string{models.Gemini25Flash},
		OpenAI: &OpenAIProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "model_routes") {
		t.Fatalf("expected model_routes required error, got %v", err)
	}
}

func TestValidateAcceptsDuplicateModelsWithExplicitRoute(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["aws_gemini"] = ProviderConfig{
		Models: []string{models.Gemini25FlashImage},
		Gemini: &GeminiProviderConfig{APIKey: "aws-key"},
	}
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini25FlashImage: "aws_gemini",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	routes := cfg.ModelRoutes()
	if routes[models.Gemini25FlashImage] != "aws_gemini" {
		t.Fatalf("expected aws_gemini route, got %#v", routes)
	}
	if routes[models.Gemini25Flash] != "gemini" {
		t.Fatalf("single-provider model should keep implicit route, got %#v", routes)
	}
}

func TestValidateRejectsUnknownRouteProvider(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["aws_gemini"] = ProviderConfig{
		Models: []string{models.Gemini25FlashImage},
		Gemini: &GeminiProviderConfig{APIKey: "aws-key"},
	}
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini25FlashImage: "missing",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected unknown provider error, got %v", err)
	}
}

func TestValidateRejectsRouteProviderMissingModel(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["aws_gemini"] = ProviderConfig{
		Models: []string{models.Gemini25FlashImage},
		Gemini: &GeminiProviderConfig{APIKey: "aws-key"},
	}
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini25FlashImage: "ark",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("expected undeclared model route error, got %v", err)
	}
}

func TestValidateRejectsRouteForUndeclaredModel(t *testing.T) {
	cfg := validConfig()
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini31FlashImage: "gemini",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "not declared by any provider") {
		t.Fatalf("expected undeclared model error, got %v", err)
	}
}

func TestValidateRejectsEmptyModel(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["ark"] = ProviderConfig{
		Models: []string{"  "},
		Ark:    &ArkProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "empty model") {
		t.Fatalf("expected empty model error, got %v", err)
	}
}

func TestValidateRejectsProviderWithoutModels(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["ark"] = ProviderConfig{
		Ark: &ArkProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "models are required") {
		t.Fatalf("expected models required error, got %v", err)
	}
}

func TestValidateRejectsEmptyDSN(t *testing.T) {
	cfg := validConfig()
	cfg.Database.DSN = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "database.dsn") {
		t.Fatalf("expected database.dsn error, got %v", err)
	}
}

func TestValidateRejectsMixedProviderKinds(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["mixed"] = ProviderConfig{
		Models: []string{"mixed-model"},
		Gemini: &GeminiProviderConfig{APIKey: "key"},
		Ark:    &ArkProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mixed provider error, got %v", err)
	}
}

func TestModelRoutes(t *testing.T) {
	routes := validConfig().ModelRoutes()
	if routes[models.Gemini25Flash] != "gemini" || routes[models.LTX] != "ltx" {
		t.Fatalf("unexpected routes %#v", routes)
	}
}

func TestProviderSupportsSpeech(t *testing.T) {
	tts := ProviderConfig{MinimaxTTS: &MinimaxTTSProviderConfig{APIKey: "k"}}
	if !ProviderSupports(tts, CapabilitySpeech) {
		t.Fatal("minimax_tts should support speech")
	}
	if ProviderSupports(tts, CapabilityText) || ProviderSupports(tts, CapabilityImage) || ProviderSupports(tts, CapabilityVideo) {
		t.Fatal("minimax_tts should not support text/image/video")
	}
}

func TestProviderSupportsOpenAIImage(t *testing.T) {
	openai := ProviderConfig{OpenAI: &OpenAIProviderConfig{APIKey: "k"}}
	if !ProviderSupports(openai, CapabilityText) || !ProviderSupports(openai, CapabilityImage) {
		t.Fatalf("openai should support text and image")
	}
	if ProviderSupports(openai, CapabilityVideo) {
		t.Fatalf("openai should not support video")
	}
}

func TestProviderSupportsVideoProviders(t *testing.T) {
	cases := []ProviderConfig{
		{DashScopeVideo: &DashScopeVideoProviderConfig{APIKey: "k"}},
		{OminilinkVideo: &OminilinkVideoProviderConfig{APIKey: "k"}},
		{GeminiVideo: &GeminiVideoProviderConfig{APIKey: "k"}},
		{ArkVideo: &ArkVideoProviderConfig{APIKey: "k"}},
		{LTX: &LTXProviderConfig{BaseURL: "https://ltx", Duration: 4, FPS: 24, PollInterval: 1, MaxPollTime: 60}},
	}
	for i, provider := range cases {
		if !ProviderSupports(provider, CapabilityVideo) {
			t.Fatalf("provider %d should support video", i)
		}
		if ProviderSupports(provider, CapabilityText) || ProviderSupports(provider, CapabilityImage) {
			t.Fatalf("provider %d should only support video", i)
		}
	}
}
