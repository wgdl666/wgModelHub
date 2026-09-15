package config

import (
	"reflect"
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

func TestLiveConfigAppliesProviderModelsAppendDeleteReorder(t *testing.T) {
	initial := validConfig()
	lc := NewLiveConfig(initial)

	// 追加：同名同资源 provider 仅 Models 变长必须热更新。
	appended := cloneConfig(initial)
	gemini := appended.Providers["gemini"]
	gemini.Models = []string{models.Gemini25Flash, models.Gemini25FlashImage, models.Gemini37Flash}
	appended.Providers["gemini"] = gemini
	lc.ApplyYAML(mustYAML(t, appended))
	routes := lc.Load().ModelRoutes()
	if routes[models.Gemini37Flash] != "gemini" {
		t.Fatalf("append routes=%v", routes)
	}

	// 删除：去掉中间模型后路由与声明同步消失。
	deleted := cloneConfig(lc.Load())
	gemini = deleted.Providers["gemini"]
	gemini.Models = []string{models.Gemini25Flash, models.Gemini37Flash}
	deleted.Providers["gemini"] = gemini
	lc.ApplyYAML(mustYAML(t, deleted))
	routes = lc.Load().ModelRoutes()
	if _, ok := routes[models.Gemini25FlashImage]; ok {
		t.Fatalf("deleted model still routed: %v", routes)
	}
	if routes[models.Gemini37Flash] != "gemini" {
		t.Fatalf("remaining routes=%v", routes)
	}

	// 重排：顺序变化不改变路由语义，且不得触发 restart_required。
	reordered := cloneConfig(lc.Load())
	gemini = reordered.Providers["gemini"]
	gemini.Models = []string{models.Gemini37Flash, models.Gemini25Flash}
	reordered.Providers["gemini"] = gemini
	if fields := RestartRequiredFields(lc.Load(), reordered); len(fields) != 0 {
		t.Fatalf("reorder must be hot-reloadable, fields=%v", fields)
	}
	lc.ApplyYAML(mustYAML(t, reordered))
	got := lc.Load().Providers["gemini"].Models
	want := []string{models.Gemini37Flash, models.Gemini25Flash}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models=%v want=%v", got, want)
	}
}

func TestLiveConfigRejectsProviderKeyAddDelete(t *testing.T) {
	initial := validConfig()
	lc := NewLiveConfig(initial)

	added := cloneConfig(initial)
	added.Providers["extra"] = ProviderConfig{
		Models: []string{"extra-model"},
		Ark:    &ArkProviderConfig{APIKey: "k"},
	}
	lc.ApplyYAML(mustYAML(t, added))
	if _, ok := lc.Load().Providers["extra"]; ok {
		t.Fatal("provider key add must be rejected")
	}

	removed := cloneConfig(initial)
	delete(removed.Providers, "ltx")
	lc.ApplyYAML(mustYAML(t, removed))
	if _, ok := lc.Load().Providers["ltx"]; !ok {
		t.Fatal("provider key delete must be rejected")
	}
}

func TestLiveConfigRejectsProviderResourceChanges(t *testing.T) {
	initial := validConfig()
	lc := NewLiveConfig(initial)

	cases := []struct {
		name string
		mut  func(Config) Config
	}{
		{
			name: "type_swap",
			mut: func(cfg Config) Config {
				p := cfg.Providers["gemini"]
				p.Gemini = nil
				p.OpenAI = &OpenAIProviderConfig{APIKey: "k"}
				cfg.Providers["gemini"] = p
				return cfg
			},
		},
		{
			name: "api_key",
			mut: func(cfg Config) Config {
				p := cfg.Providers["ark"]
				p.Ark = &ArkProviderConfig{APIKey: "changed-key", BaseURL: p.Ark.BaseURL}
				cfg.Providers["ark"] = p
				return cfg
			},
		},
		{
			name: "base_url",
			mut: func(cfg Config) Config {
				p := cfg.Providers["gemini"]
				p.Gemini = &GeminiProviderConfig{APIKey: p.Gemini.APIKey, BaseURL: "https://changed.example"}
				cfg.Providers["gemini"] = p
				return cfg
			},
		},
		{
			name: "poll_interval",
			mut: func(cfg Config) Config {
				p := cfg.Providers["ltx"]
				p.LTX = &LTXProviderConfig{
					BaseURL:      p.LTX.BaseURL,
					Token:        p.LTX.Token,
					Duration:     p.LTX.Duration,
					FPS:          p.LTX.FPS,
					Seed:         p.LTX.Seed,
					PollInterval: 9,
					MaxPollTime:  p.LTX.MaxPollTime,
				}
				cfg.Providers["ltx"] = p
				return cfg
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := tc.mut(cloneConfig(initial))
			if fields := RestartRequiredFields(initial, next); len(fields) == 0 || fields[0] != "providers" {
				t.Fatalf("fields=%v", fields)
			}
			lc.ApplyYAML(mustYAML(t, next))
			if reflect.DeepEqual(lc.Load().Providers, next.Providers) {
				t.Fatal("resource change must keep previous providers")
			}
		})
	}
}

func TestLiveConfigRejectsMixedModelsAndResourceChange(t *testing.T) {
	initial := validConfig()
	lc := NewLiveConfig(initial)

	next := cloneConfig(initial)
	gemini := next.Providers["gemini"]
	gemini.Models = append(append([]string{}, gemini.Models...), models.Gemini37Flash)
	gemini.Gemini = &GeminiProviderConfig{APIKey: "changed-key", BaseURL: gemini.Gemini.BaseURL}
	next.Providers["gemini"] = gemini

	if fields := RestartRequiredFields(initial, next); len(fields) == 0 {
		t.Fatal("mixed change must require restart")
	}
	lc.ApplyYAML(mustYAML(t, next))
	got := lc.Load()
	if _, ok := got.ModelRoutes()[models.Gemini37Flash]; ok {
		t.Fatal("mixed change must not partially apply new model")
	}
	if got.Providers["gemini"].Gemini.APIKey == "changed-key" {
		t.Fatal("mixed change must not apply credential")
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
	initial.Server.HTTPListenAddress = ":51053"
	lc := NewLiveConfig(initial)

	next := initial
	next.ModelRouteOverrides = map[string]string{models.Gemini25Flash: "gemini_backup"}
	hotYAML := mustYAML(t, next)
	if strings.Contains(hotYAML, "listen_address") || strings.Contains(hotYAML, "public_listen_address") || strings.Contains(hotYAML, "http_listen_address") {
		t.Fatalf("serialized YAML must not contain listen addresses: %q", hotYAML)
	}

	lc.ApplyYAML(hotYAML)
	got := lc.Load()
	if got.ModelRoutes()[models.Gemini25Flash] != "gemini_backup" {
		t.Fatalf("hot field not applied: routes=%v", got.ModelRoutes())
	}
	if got.Server.ListenAddress != ":50053" || got.Server.PublicListenAddress != ":50054" || got.Server.HTTPListenAddress != ":51053" {
		t.Fatalf("listen addresses=%q/%q/%q, want env-injected :50053/:50054/:51053",
			got.Server.ListenAddress, got.Server.PublicListenAddress, got.Server.HTTPListenAddress)
	}
}

func TestApplyListenPortOverridesFromEnv(t *testing.T) {
	cfg := validConfig()
	t.Setenv("WG_SERVER_GRPC_PORT", "50053")
	t.Setenv("WG_SERVER_HTTP_PORT", "51053")
	if err := ApplyListenPortOverridesFromEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddress != ":50053" {
		t.Fatalf("listen_address=%q", cfg.Server.ListenAddress)
	}
	if cfg.Server.HTTPListenAddress != ":51053" {
		t.Fatalf("http listen_address=%q", cfg.Server.HTTPListenAddress)
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
	t.Setenv("WG_SERVER_GRPC_PORT", "50053")
	t.Setenv("WG_SERVER_HTTP_PORT", "")
	if err := ApplyListenPortOverridesFromEnv(&cfg); err == nil {
		t.Fatal("missing HTTP port must fail server assembly")
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
			next := cloneConfig(initial)
			next.ModelRouteOverrides = map[string]string{models.Gemini25Flash: selected}
			lc.ApplyYAML(mustYAML(t, next))
		}(provider)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := cloneConfig(initial)
			gemini := next.Providers["gemini"]
			if i%2 == 0 {
				gemini.Models = []string{models.Gemini25Flash, models.Gemini25FlashImage, models.Gemini37Flash}
			} else {
				gemini.Models = []string{models.Gemini25FlashImage, models.Gemini25Flash}
			}
			next.Providers["gemini"] = gemini
			lc.ApplyYAML(mustYAML(t, next))
		}(i)
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

func cloneConfig(cfg Config) Config {
	body, err := yaml.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	out, err := ParseAndValidateYAML(string(body))
	if err != nil {
		panic(err)
	}
	out.Server = cfg.Server
	return out
}

func mustYAML(t *testing.T, cfg Config) string {
	t.Helper()
	content, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
