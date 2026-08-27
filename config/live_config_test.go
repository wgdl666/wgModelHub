package config

import (
	"strings"
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

func TestConfigYAMLOmitsListenAddresses(t *testing.T) {
	content := mustYAML(t, validConfig())
	if strings.Contains(content, "listen_address") || strings.Contains(content, "public_listen_address") {
		t.Fatalf("serialized YAML must not contain listen addresses: %q", content)
	}
}

func TestLiveConfigPreservesEnvListenAddressesOnHotReload(t *testing.T) {
	initial := validConfigWithDualGeminiFlash()
	initial.Server.ListenAddress = ":50053"
	initial.Server.PublicListenAddress = ":50054"
	lc := NewLiveConfig(initial)

	next := initial
	next.ModelRouteOverrides = map[string]string{models.Gemini25Flash: "gemini_backup"}
	hotYAML := mustYAML(t, next)
	if strings.Contains(hotYAML, "listen_address") || strings.Contains(hotYAML, "public_listen_address") {
		t.Fatalf("serialized YAML must not contain listen addresses: %q", hotYAML)
	}

	lc.ApplyYAML(hotYAML)
	got := lc.Load()
	if got.ModelRoutes()[models.Gemini25Flash] != "gemini_backup" {
		t.Fatalf("hot field not applied: routes=%v", got.ModelRoutes())
	}
	if got.Server.ListenAddress != ":50053" || got.Server.PublicListenAddress != ":50054" {
		t.Fatalf("listen addresses=%q/%q, want env-injected :50053/:50054",
			got.Server.ListenAddress, got.Server.PublicListenAddress)
	}
}

func TestApplyListenPortOverridesFromEnv(t *testing.T) {
	cfg := validConfig()
	t.Setenv("WG_SERVER_GRPC_PORT", "50053")
	if err := ApplyListenPortOverridesFromEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddress != ":50053" {
		t.Fatalf("listen_address=%q", cfg.Server.ListenAddress)
	}
	if cfg.Server.PublicListenAddress != "" {
		t.Fatalf("public listener should default off, got %q", cfg.Server.PublicListenAddress)
	}
	t.Setenv("WG_SERVER_PUBLIC_GRPC_PORT", "50054")
	if err := ApplyListenPortOverridesFromEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.PublicListenAddress != ":50054" {
		t.Fatalf("public listen_address=%q", cfg.Server.PublicListenAddress)
	}
	t.Setenv("WG_SERVER_GRPC_PORT", "")
	if err := ApplyListenPortOverridesFromEnv(&cfg); err == nil {
		t.Fatal("missing env port must fail server assembly")
	}
}

func TestLiveConfigRejectsRestartRequiredFields(t *testing.T) {
	initial := validConfigWithDualGeminiFlash()
	lc := NewLiveConfig(initial)

	next := initial
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
