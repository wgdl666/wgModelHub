package config

import (
	"os"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
	"gopkg.in/yaml.v3"
)

func TestDockerContextIncludesExampleYAMLForBuilderTests(t *testing.T) {
	raw, err := os.ReadFile("../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\n!config/example.modelHub.yaml\n") {
		t.Fatal(".dockerignore must re-include config/example.modelHub.yaml for Docker builder tests")
	}
}

func TestDockerfileDefaultsToProductionStage(t *testing.T) {
	raw, err := os.ReadFile("../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "FROM runtime AS production" {
		t.Fatalf("Dockerfile final stage=%q, want production", got)
	}
}

func TestExampleYAMLUsesExactlyKnownModelIDs(t *testing.T) {
	raw, err := os.ReadFile("example.modelHub.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]struct {
			Models []string `yaml:"models"`
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
}
