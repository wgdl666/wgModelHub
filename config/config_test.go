package config

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/internal/profile"
)

func validConfig() Config {
	cfg := Config{
		Server: struct {
			ListenAddress string `yaml:"listen_address"`
		}{ListenAddress: ":50053"},
		Providers: map[string]ProviderConfig{
			"ark":    {Type: "ark", APIKey: "key"},
			"gemini": {Type: "gemini", APIKey: "key"},
			"ltx": {
				Type:         "ltx",
				BaseURL:      "https://ltx.example",
				Token:        "token",
				Duration:     4,
				FPS:          24,
				PollInterval: 2,
				MaxPollTime:  300,
			},
		},
		Profiles: map[string]ProfileConfig{},
	}
	for _, name := range profile.Required() {
		switch {
		case strings.HasPrefix(name, "hub."):
			cfg.Profiles[name] = ProfileConfig{Capability: CapabilityText, Provider: "ark", Model: "model"}
		case name == profile.AsyncLTXVideo:
			cfg.Profiles[name] = ProfileConfig{Capability: CapabilityVideo, Provider: "ltx", Model: "ltx"}
		default:
			cfg.Profiles[name] = ProfileConfig{Capability: CapabilityImage, Provider: "gemini", Model: "image-model"}
		}
	}
	return cfg
}

func TestValidateRequiresAllProfiles(t *testing.T) {
	cfg := validConfig()
	delete(cfg.Profiles, profile.HubChat)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), profile.HubChat) {
		t.Fatalf("expected missing profile error, got %v", err)
	}
}

func TestValidateAcceptsCompleteProfiles(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}
