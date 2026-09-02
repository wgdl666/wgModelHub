package config

import (
	"os"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
	"gopkg.in/yaml.v3"
)

func TestExampleYAMLUsesExactlyKnownModelIDs(t *testing.T) {
	raw, err := os.ReadFile("example.modelHub.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]struct {
			Models []string `yaml:"models"`
			Ark    *struct {
				EndpointID string `yaml:"endpoint_id"`
			} `yaml:"ark"`
		} `yaml:"providers"`
		ModelRoutes map[string]string `yaml:"model_routes"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	// 允许双渠道声明同一真实模型，但必须有 model_routes 显式选定，且目标实例确实声明了该模型。
	declaredBy := map[string]map[string]struct{}{}
	fromYAML := map[string]struct{}{}
	for name, provider := range parsed.Providers {
		for _, model := range provider.Models {
			fromYAML[model] = struct{}{}
			if declaredBy[model] == nil {
				declaredBy[model] = map[string]struct{}{}
			}
			declaredBy[model][name] = struct{}{}
		}
	}
	for model, providers := range declaredBy {
		if len(providers) == 1 {
			continue
		}
		selected, ok := parsed.ModelRoutes[model]
		if !ok {
			t.Fatalf("example yaml multi-binds %s without model_routes", model)
		}
		if _, ok := providers[selected]; !ok {
			t.Fatalf("example model_routes[%s]=%s is not among declaring providers", model, selected)
		}
	}
	known := map[string]struct{}{}
	for _, model := range models.All() {
		known[model] = struct{}{}
		if _, ok := fromYAML[model]; !ok {
			t.Fatalf("known model %s is missing from example.modelHub.yaml", model)
		}
	}
	for model := range fromYAML {
		if _, ok := known[model]; !ok {
			t.Fatalf("example.modelHub.yaml has undocumented model %s", model)
		}
	}

	// 2.1 Pro 在示例配置中绑定独立 Ark endpoint，供衣橱候选搭配链路路由。
	provider, ok := parsed.Providers["ark_doubao_21_pro"]
	if !ok {
		t.Fatal("missing provider ark_doubao_21_pro")
	}
	if len(provider.Models) != 1 || provider.Models[0] != models.DoubaoSeed21Pro {
		t.Fatalf("ark_doubao_21_pro models=%v, want [%q]", provider.Models, models.DoubaoSeed21Pro)
	}
	if provider.Ark == nil {
		t.Fatal("ark_doubao_21_pro ark.endpoint_id is missing")
	}
	if provider.Ark.EndpointID != "ep-20260902131944-rj4cb" {
		t.Fatalf("ark_doubao_21_pro endpoint_id=%q, want ep-20260902131944-rj4cb", provider.Ark.EndpointID)
	}
}
