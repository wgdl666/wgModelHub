package config

import (
	"sync"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
	"gopkg.in/yaml.v3"
)

func TestLiveConfigAppliesModelRoutes(t *testing.T) {
	initial := validConfigWithDualGeminiFlash()
	lc := NewLiveConfig(initial)

	next := initial
	next.ModelRouteOverrides = map[string]string{models.Gemini25Flash: "gemini_backup"}
	lc.ApplyYAML(mustYAML(t, next))
	routes := lc.Load().ModelRoutes()
	if routes[models.Gemini25Flash] != "gemini_backup" {
		t.Fatalf("routes=%v", routes)
	}
}

func TestLiveConfigRejectsInvalidYAML(t *testing.T) {
	initial := validConfig()
	lc := NewLiveConfig(initial)
	lc.ApplyYAML("server: [\n")
	if lc.Load().Server.ListenAddress != initial.Server.ListenAddress {
		t.Fatal("invalid yaml must keep previous config")
	}
}

func TestLiveConfigRejectsRestartRequiredFields(t *testing.T) {
	initial := validConfigWithDualGeminiFlash()
	lc := NewLiveConfig(initial)

	next := initial
	next.Server.ListenAddress = ":50099"
	next.ModelRouteOverrides = map[string]string{models.Gemini25Flash: "gemini_backup"}
	lc.ApplyYAML(mustYAML(t, next))
	if lc.Load().Server.ListenAddress != initial.Server.ListenAddress {
		t.Fatal("mixed restart document must not apply hot half")
	}

	next = initial
	body := mustYAML(t, initial)
	next, err := ParseAndValidateYAML(body)
	if err != nil {
		t.Fatal(err)
	}
	p := next.Providers["ark"]
	if p.Ark == nil {
		t.Fatal("expected ark provider")
	}
	p.Ark = &ArkProviderConfig{APIKey: "changed-key", BaseURL: p.Ark.BaseURL}
	next.Providers["ark"] = p
	lc.ApplyYAML(mustYAML(t, next))
	if lc.Load().Providers["ark"].Ark.APIKey == "changed-key" {
		t.Fatal("provider credential change must be rejected")
	}
}

func TestLiveConfigConcurrentLoadStore(t *testing.T) {
	initial := validConfigWithDualGeminiFlash()
	lc := NewLiveConfig(initial)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		provider := "gemini"
		if i%2 == 1 {
			provider = "gemini_backup"
		}
		go func(selected string) {
			defer wg.Done()
			next := initial
			next.ModelRouteOverrides = map[string]string{models.Gemini25Flash: selected}
			lc.ApplyYAML(mustYAML(t, next))
		}(provider)
	}
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = lc.Load().ModelRoutes()
		}()
	}
	wg.Wait()
}

func validConfigWithDualGeminiFlash() Config {
	cfg := validConfig()
	cfg.Providers["gemini_backup"] = ProviderConfig{
		Models: []string{models.Gemini25Flash},
		Gemini: &GeminiProviderConfig{APIKey: "backup-key"},
	}
	cfg.ModelRouteOverrides = map[string]string{models.Gemini25Flash: "gemini"}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func mustYAML(t *testing.T, cfg Config) string {
	t.Helper()
	content, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
