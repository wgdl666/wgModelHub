package config

import (
	"strings"
	"testing"
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
				Models: []string{"gemini-2.5-flash", "gemini-2.5-flash-image"},
				Gemini: &GeminiProviderConfig{APIKey: "key"},
			},
			"ltx": {
				Models: []string{"ltx"},
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
		Models: []string{"gemini-2.5-flash"},
		OpenAI: &OpenAIProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "gemini-2.5-flash") {
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
	if routes["gemini-2.5-flash"] != "gemini" || routes["ltx"] != "ltx" {
		t.Fatalf("unexpected routes %#v", routes)
	}
}
