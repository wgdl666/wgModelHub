package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

// 海外 AppConfig 与 Nacos 共用模型配置契约，快速版和官方 latest ID 无需单独的 model 覆盖字段。
func TestGoogleRerankModelsFromAppConfig(t *testing.T) {
	want := validConfig()
	want.Providers = map[string]ProviderConfig{"google": {
		Models:       []string{models.GoogleSemanticRankerFast005, models.GoogleSemanticRankerDefaultLatest, models.GoogleSemanticRankerFastLatest},
		GoogleRerank: &GoogleRerankProviderConfig{Project: "test-project"},
	}}
	raw := mustYAML(t, want)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(raw)) }))
	defer server.Close()
	setValidAppConfigEnv(t, server.URL)
	loader, err := NewAppConfigLoaderFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := loader.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	routes := got.ModelRoutes()
	if len(routes) != 3 {
		t.Fatalf("routes=%v", routes)
	}
	for _, model := range want.Providers["google"].Models {
		if routes[model] != "google" {
			t.Fatalf("model %s route=%q", model, routes[model])
		}
	}
	if _, ok := routes[models.GoogleSemanticRankerDefault005]; ok {
		t.Fatal("unconfigured model must not be enabled implicitly")
	}
}

func TestGoogleRerankConfigAndRouting(t *testing.T) {
	for _, tc := range []struct {
		project, proxy string
		invalid        bool
	}{
		{"test-project", "", false}, {"686930556106", "http://127.0.0.1:1081", false},
		{"", "", true}, {"bad/project", "", true}, {"bad project", "", true},
		{"test-project", "not-a-url", true}, {"test-project", "ftp://proxy.local", true},
	} {
		cfg := validConfig()
		cfg.Providers["google"] = ProviderConfig{Models: []string{models.GoogleSemanticRankerDefault005}, GoogleRerank: &GoogleRerankProviderConfig{Project: tc.project, ProxyURL: tc.proxy}}
		err := cfg.Validate()
		if (err != nil) != tc.invalid {
			t.Errorf("project=%q proxy=%q err=%v", tc.project, tc.proxy, err)
		}
		if tc.invalid {
			continue
		}
		p := cfg.Providers["google"]
		if cfg.ModelRoutes()[models.GoogleSemanticRankerDefault005] != "google" || !ProviderSupports(p, CapabilityText) || ProviderSupports(p, CapabilityImage) || ProviderSupports(p, CapabilityVideo) {
			t.Fatal("google rerank routing/capabilities incorrect")
		}
		// 精排连接资源必须整体重启；仅模型列表的变动才可热应用。
		next := cfg
		next.Providers = map[string]ProviderConfig{}
		for k, v := range cfg.Providers {
			next.Providers[k] = v
		}
		p.GoogleRerank = &GoogleRerankProviderConfig{Project: "other-project", ProxyURL: tc.proxy}
		next.Providers["google"] = p
		if !strings.Contains(strings.Join(RestartRequiredFields(cfg, next), ","), "providers") {
			t.Fatal("project changes must require restart")
		}
		p = cfg.Providers["google"]
		p.Models = nil
		next.Providers["google"] = p
		if len(RestartRequiredFields(cfg, next)) != 0 {
			t.Fatal("model routing changes must stay hot reloadable")
		}
		p.Ark = &ArkProviderConfig{APIKey: "k"}
		next.Providers["google"] = p
		if validateProvider("google", p) == nil {
			t.Fatal("two concrete provider kinds must be rejected")
		}
	}
}
