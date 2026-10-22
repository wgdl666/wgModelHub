package config

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func TestExplicitCacheModelScope(t *testing.T) {
	policy := ExplicitCacheConfig{Enabled: true, Models: []string{models.Gemini37Flash, models.Gemini38Flash}}
	for _, model := range []string{models.Gemini37Flash, models.Gemini38Flash} {
		if !policy.EnabledFor(model) {
			t.Fatalf("selected model %s is disabled", model)
		}
	}
	if policy.EnabledFor(models.Gemini25Flash) {
		t.Fatal("unselected model must bypass automatic caching")
	}
	policy.Enabled = false
	if policy.EnabledFor(models.Gemini38Flash) {
		t.Fatal("master switch must override selected models")
	}
	policy.Enabled, policy.Models = true, nil
	if !policy.EnabledFor(models.Gemini25Flash) {
		t.Fatal("omitted model list must preserve unrestricted policy")
	}
}

func TestExplicitCacheSwitchDefaultsOffAndHotReloads(t *testing.T) {
	initial := validConfig()
	if initial.ExplicitCache.Enabled {
		t.Fatal("managed explicit cache must default off")
	}
	live := NewLiveConfig(initial)
	enabled := cloneConfig(initial)
	enabled.ExplicitCache.Enabled = true
	enabled.ExplicitCache.Models = []string{models.Gemini37Flash, models.Gemini38Flash}
	if fields := RestartRequiredFields(initial, enabled); len(fields) != 0 {
		t.Fatalf("cache policy must be hot reloadable: %v", fields)
	}
	live.ApplyYAML(mustYAML(t, enabled))
	if !live.Load().ExplicitCache.Enabled {
		t.Fatal("explicit_cache.enabled not parsed/applied")
	}
	if live.Load().ExplicitCache.EnabledFor(models.Gemini25Flash) || !live.Load().ExplicitCache.EnabledFor(models.Gemini38Flash) {
		t.Fatal("model restriction not parsed/applied")
	}
	live.ApplyYAML(mustYAML(t, initial))
	if live.Load().ExplicitCache.Enabled {
		t.Fatal("cache policy did not turn off")
	}
	invalid := strings.Replace(mustYAML(t, initial), "enabled: false", "enabled: [invalid]", 1)
	if _, err := ParseAndValidateYAML(invalid); err == nil {
		t.Fatal("invalid cache policy must be rejected")
	}
}
