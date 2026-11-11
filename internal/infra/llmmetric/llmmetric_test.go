package llmmetric_test

import (
	"context"
	"errors"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
	"github.com/wgdl666/wgModelHub/internal/provider"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyCache(t *testing.T) {
	if got := llmmetric.ClassifyCache("default_enabled", &modelhubv2.Usage{CachedTokens: 3}); got != llmmetric.CacheHit {
		t.Fatalf("hit=%q", got)
	}
	if got := llmmetric.ClassifyCache(llmmetric.CachingModeExplicitDisabled, &modelhubv2.Usage{CachedTokens: 0}); got != llmmetric.CacheDisabled {
		t.Fatalf("disabled=%q", got)
	}
	if got := llmmetric.ClassifyCache("implicit_automatic", &modelhubv2.Usage{CachedTokens: 0}); got != llmmetric.CacheMiss {
		t.Fatalf("ark implicit miss=%q", got)
	}
	if got := llmmetric.ClassifyCache("default_enabled", nil); got != llmmetric.CacheUnknown {
		t.Fatalf("unknown=%q", got)
	}
}

func TestMapOutcome(t *testing.T) {
	if got := llmmetric.MapOutcome(nil); got != llmmetric.OutcomeSucceeded {
		t.Fatalf("nil=%q", got)
	}
	if got := llmmetric.MapOutcome(context.Canceled); got != llmmetric.OutcomeCancelled {
		t.Fatalf("context canceled=%q", got)
	}
	if got := llmmetric.MapOutcome(context.DeadlineExceeded); got != llmmetric.OutcomeFailed {
		t.Fatalf("context deadline=%q", got)
	}
	if got := llmmetric.MapOutcome(status.Error(codes.DeadlineExceeded, "deadline")); got != llmmetric.OutcomeFailed {
		t.Fatalf("deadline=%q", got)
	}
	if got := llmmetric.MapOutcome(status.Error(codes.Canceled, "canceled")); got != llmmetric.OutcomeCancelled {
		t.Fatalf("grpc canceled=%q", got)
	}
	if got := llmmetric.MapOutcome(errors.New("boom")); got != llmmetric.OutcomeFailed {
		t.Fatalf("failed=%q", got)
	}
}

func TestIsFirstModelOutput(t *testing.T) {
	if llmmetric.IsFirstModelOutput(&modelhubv2.GenerateEvent{}) {
		t.Fatal("empty must be false")
	}
	if llmmetric.IsFirstModelOutput(provider.TextDeltaEvent("  ")) {
		t.Fatal("whitespace text must be false")
	}
	if !llmmetric.IsFirstModelOutput(provider.TextDeltaEvent("hi")) {
		t.Fatal("non-empty text must be true")
	}
	if llmmetric.IsFirstModelOutput(provider.ToolCallEvent(&modelhubv2.ToolCall{ArgumentsJson: []byte("{}")})) {
		t.Fatal("arguments-only tool delta must be false")
	}
	if !llmmetric.IsFirstModelOutput(provider.ToolCallEvent(&modelhubv2.ToolCall{Name: "closet_search"})) {
		t.Fatal("tool name must be true")
	}
}

func TestRecordImageTerminalExportsOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if err := llmmetric.Configure(provider.Meter("test")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	llmmetric.RecordImageTerminal(ctx, "gpt-image-2", "openai", llmmetric.OutcomeSucceeded)
	llmmetric.RecordImageTerminal(ctx, "gpt-image-2", "openai", llmmetric.OutcomeFailed)
	llmmetric.RecordImageTerminal(ctx, "gpt-image-2", "openai", llmmetric.OutcomeSucceeded)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "wg.modelhub.llm.image.terminals" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				outcome, _ := dp.Attributes.Value("outcome")
				model, _ := dp.Attributes.Value("model")
				if model.AsString() != "gpt-image-2" {
					t.Fatalf("model=%s", model.AsString())
				}
				got[outcome.AsString()] = dp.Value
			}
		}
	}
	if got[llmmetric.OutcomeSucceeded] != 2 || got[llmmetric.OutcomeFailed] != 1 {
		t.Fatalf("got=%v", got)
	}
}
