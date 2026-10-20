package config

import (
	"fmt"
	"strings"
	"testing"
)

func embeddingTimeoutYAML(timeout string) string {
	return fmt.Sprintf(`database: {dsn: "postgres://unused/unused"}
logfire: {token: unused}
providers:
  embedding:
    models: [qwen3-vl-embedding]
    dashscope_embedding:
      base_url: https://embedding.invalid/api/v1
      api_key: unused
      http_timeout: %q
`, timeout)
}

func TestDashScopeEmbeddingRejectsInvalidHTTPTimeout(t *testing.T) {
	for _, value := range []string{"0s", "-1s", "not-a-duration", "999999999999999999999h"} {
		t.Run(value, func(t *testing.T) {
			_, err := ParseAndValidateYAML(embeddingTimeoutYAML(value))
			if err == nil || !strings.Contains(err.Error(), "http_timeout") {
				t.Fatalf("invalid http_timeout must fail config validation, got %v", err)
			}
		})
	}
}

func TestDashScopeEmbeddingTimeoutRequiresColdRestart(t *testing.T) {
	initial, err := ParseAndValidateYAML(embeddingTimeoutYAML(""))
	if err != nil {
		t.Fatal(err)
	}
	next, err := ParseAndValidateYAML(embeddingTimeoutYAML("120s"))
	if err != nil {
		t.Fatal(err)
	}
	if fields := RestartRequiredFields(initial, next); len(fields) != 1 || fields[0] != "providers" {
		t.Fatalf("timeout change must require provider rebuild, fields=%v", fields)
	}
	live := NewLiveConfig(initial)
	live.ApplyYAML(embeddingTimeoutYAML("120s"))
	if fields := RestartRequiredFields(live.Load(), initial); len(fields) != 0 {
		t.Fatalf("rejected hot update changed active resources: %v", fields)
	}
}
