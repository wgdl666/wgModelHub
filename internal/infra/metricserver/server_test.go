package metricserver_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
	"github.com/wgdl666/wgModelHub/internal/infra/metricserver"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestPrometheusExporterContract(t *testing.T) {
	reader, err := metricserver.NewReader()
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader.ManualReader()))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	if err := llmmetric.Configure(provider.Meter("metricserver-contract")); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	labels := llmmetric.Labels{Model: "chat-model", Provider: "ark", Stream: true}
	llmmetric.RecordRequest(ctx, labels)
	llmmetric.RecordTTFT(ctx, labels, 250*time.Millisecond)
	labels.Outcome = llmmetric.OutcomeSucceeded
	llmmetric.RecordTerminal(ctx, labels, 1500*time.Millisecond)
	llmmetric.RecordUsageAndCache(ctx, labels, llmmetric.CachingModeExplicitDisabled, &modelhubv2.Usage{
		InputTokens: 10, OutputTokens: 2, TotalTokens: 12, CachedTokens: 0, ReasoningTokens: 1,
	})

	ts := httptest.NewServer(reader.Handler())
	t.Cleanup(ts.Close)
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)

	required := []string{
		"wg_modelhub_llm_requests_total",
		"wg_modelhub_llm_terminals_total",
		"wg_modelhub_llm_ttft_seconds",
		"wg_modelhub_llm_duration_seconds",
		"wg_modelhub_llm_input_tokens_total",
		"wg_modelhub_llm_output_tokens_total",
		"wg_modelhub_llm_tokens_reported_total",
		"wg_modelhub_llm_cached_tokens_total",
		"wg_modelhub_llm_reasoning_tokens_total",
		"wg_modelhub_llm_usage_reports_total",
		"wg_modelhub_llm_cache_requests_total",
	}
	for _, name := range required {
		if !strings.Contains(text, name) {
			t.Fatalf("missing metric name %s\n%s", name, text)
		}
	}

	forbidden := []string{
		`user_id=`, `session_id=`, `turn_id=`, `task_id=`, `trace_id=`, `request_id=`,
		`prompt=`, `error=`,
	}
	for _, bad := range forbidden {
		if strings.Contains(text, bad) {
			t.Fatalf("forbidden high-cardinality label fragment %q present", bad)
		}
	}

	allowedLabelRE := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="`)
	allowed := map[string]struct{}{
		"model": {}, "provider": {}, "stream": {}, "outcome": {}, "state": {}, "result": {}, "le": {},
	}
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "wg_modelhub_llm_") || strings.HasPrefix(line, "#") {
			continue
		}
		for _, m := range allowedLabelRE.FindAllStringSubmatch(line, -1) {
			key := m[1]
			if _, ok := allowed[key]; !ok {
				t.Fatalf("unexpected label %q in line: %s", key, line)
			}
		}
	}

	assertHistogramBuckets(t, text, "wg_modelhub_llm_ttft_seconds_bucket",
		[]string{"0.1", "0.25", "0.5", "1", "2", "3", "5", "8", "13", "20", "30", "60", "+Inf"})
	assertHistogramBuckets(t, text, "wg_modelhub_llm_duration_seconds_bucket",
		[]string{"0.25", "0.5", "1", "2", "3", "5", "8", "13", "20", "30", "60", "120", "+Inf"})
}

func assertHistogramBuckets(t *testing.T, text, metric string, want []string) {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(metric) + `\{[^}]*le="([^"]+)"[^}]*\}`)
	found := map[string]struct{}{}
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		found[m[1]] = struct{}{}
	}
	for _, le := range want {
		if _, ok := found[le]; !ok {
			t.Fatalf("%s missing bucket le=%s; found=%v", metric, le, found)
		}
	}
}
