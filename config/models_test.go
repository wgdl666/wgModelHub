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
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	fromYAML := map[string]string{}
	for name, provider := range parsed.Providers {
		for _, model := range provider.Models {
			if other, ok := fromYAML[model]; ok {
				t.Fatalf("example yaml binds %s to both %s and %s", model, other, name)
			}
			fromYAML[model] = name
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
