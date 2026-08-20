package config

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func validConfig() Config {
	return Config{
		Server: struct {
			ListenAddress string `yaml:"listen_address"`
		}{ListenAddress: ":50053"},
		Logfire: LogfireConfig{
			Token:   "logfire-token",
			Env:     "production",
			Service: "wg-model-hub",
		},
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

func TestValidateRejectsDuplicateModels(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["other"] = ProviderConfig{
		Models: []string{models.Gemini25Flash},
		OpenAI: &OpenAIProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), models.Gemini25Flash) {
		t.Fatalf("expected duplicate model error, got %v", err)
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

func TestProviderSupportsOpenAIImage(t *testing.T) {
	openai := ProviderConfig{OpenAI: &OpenAIProviderConfig{APIKey: "k"}}
	if !ProviderSupports(openai, CapabilityText) || !ProviderSupports(openai, CapabilityImage) {
		t.Fatalf("openai should support text and image")
	}
	if ProviderSupports(openai, CapabilityVideo) {
		t.Fatalf("openai should not support video")
	}
}
