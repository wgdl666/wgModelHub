package config

import (
	"github.com/wgdl666/wgModelHub/models"
	"testing"
)

func TestOpenAIInternalAuth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		auth  OpenAIProviderConfig
		valid bool
	}{
		{"internal", OpenAIProviderConfig{NoAuth: true, BaseURL: "http://gemma.model3.wgdl.internal/v1"}, true},
		{"missing key", OpenAIProviderConfig{BaseURL: "http://gemma.model3.wgdl.internal/v1"}, false},
		{"conflicting key", OpenAIProviderConfig{NoAuth: true, APIKey: "secret", BaseURL: "http://gemma.model3.wgdl.internal/v1"}, false},
		{"implicit public endpoint", OpenAIProviderConfig{NoAuth: true}, false},
		{"bearer", OpenAIProviderConfig{APIKey: "secret"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Providers = map[string]ProviderConfig{"wgdl_gemma": {Models: []string{models.Gemma426BA4BIT}, OpenAI: &tc.auth}}
			if err := cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate()=%v, valid=%v", err, tc.valid)
			}
		})
	}
}
