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

func TestParseAndValidateYAMLWithoutListenAddress(t *testing.T) {
	content, err := yaml.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "server")
	content, err = yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAndValidateYAML(string(content)); err != nil {
		t.Fatalf("Nacos without server section must parse: %v", err)
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
	initial.Server.ListenAddress = ":50053"
	lc := NewLiveConfig(initial)

	next := initial
	next.Server.ListenAddress = ":50099"
	next.ModelRouteOverrides = map[string]string{models.Gemini25Flash: "gemini_backup"}
	lc.ApplyYAML(mustYAML(t, next))
	got := lc.Load()
	if got.ModelRoutes()[models.Gemini25Flash] != "gemini_backup" {
		t.Fatal("listen_address in Nacos YAML must be ignored; hot fields should still apply")
	}
	if got.Server.ListenAddress != ":50053" {
		t.Fatalf("runtime listen_address=%q, want env value :50053", got.Server.ListenAddress)
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
