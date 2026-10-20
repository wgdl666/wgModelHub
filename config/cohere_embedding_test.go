package config

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func TestCohereExecutionLimitsDefaultAndValidate(t *testing.T) {
	defaults := (CohereEmbeddingProviderConfig{}).Resolved()
	if defaults.MaxBatchSize != 96 || defaults.MaxConcurrency != 4 || defaults.MaxRequestTexts != 512 || defaults.MaxRequestTextBytes != 1<<20 || defaults.RequestTimeoutSeconds != 30 {
		t.Fatalf("defaults=%+v", defaults)
	}
	for _, cfg := range []CohereEmbeddingProviderConfig{
		{MaxBatchSize: 97}, {MaxBatchSize: -1}, {MaxConcurrency: -1}, {MaxConcurrency: 65},
		{MaxRequestTexts: -1}, {MaxRequestTexts: 1025}, {MaxRequestTextBytes: -1}, {MaxRequestTextBytes: 4<<20 + 1}, {RequestTimeoutSeconds: -1}, {RequestTimeoutSeconds: 121},
	} {
		if cfg.ValidateExecutionLimits() == nil {
			t.Errorf("accepted invalid execution limits %+v", cfg)
		}
	}
}
func TestCohereLimitsAreWiredAndRequireRestart(t *testing.T) {
	cfg := validConfig()
	cfg.Providers = map[string]ProviderConfig{"cohere": {Models: []string{models.CohereEmbedV4}, CohereEmbedding: &CohereEmbeddingProviderConfig{BaseURL: "https://api.cohere.com", APIKey: "test"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	next := cloneConfig(cfg)
	next.Providers["cohere"].CohereEmbedding.MaxBatchSize = 32
	if fields := RestartRequiredFields(cfg, next); !strings.Contains(strings.Join(fields, ","), "providers") {
		t.Fatalf("limits hot-applied without rebuilding gate: %v", fields)
	}
	live := NewLiveConfig(cfg)
	live.ApplyYAML(mustYAML(t, next))
	if live.Load().Providers["cohere"].CohereEmbedding.MaxBatchSize != 0 {
		t.Fatal("unsupported hot limit change applied")
	}
	next.Providers["cohere"].CohereEmbedding.MaxBatchSize = 97
	if next.Validate() == nil {
		t.Fatal("top-level validation omitted execution limits")
	}
}
