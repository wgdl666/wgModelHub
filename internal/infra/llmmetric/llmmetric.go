package llmmetric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"

	UsagePresent = "present"
	UsageMissing = "missing"

	CacheHit      = "hit"
	CacheMiss     = "miss"
	CacheDisabled = "disabled"
	CacheUnknown  = "unknown"

	// 与 applyTextCachingPolicy 返回值对齐；仅 explicit_disabled 才能标 cache=disabled。
	CachingModeExplicitDisabled = "explicit_disabled"
)

var (
	ttftBuckets     = []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 13, 20, 30, 60}
	durationBuckets = []float64{0.25, 0.5, 1, 2, 3, 5, 8, 13, 20, 30, 60, 120}
)

type recorder struct {
	requests        otelmetric.Int64Counter
	terminals       otelmetric.Int64Counter
	ttft            otelmetric.Float64Histogram
	duration        otelmetric.Float64Histogram
	inputTokens     otelmetric.Int64Counter
	outputTokens    otelmetric.Int64Counter
	reportedTokens  otelmetric.Int64Counter
	cachedTokens    otelmetric.Int64Counter
	reasoningTokens otelmetric.Int64Counter
	usageReports    otelmetric.Int64Counter
	cacheRequests   otelmetric.Int64Counter
}

var current atomic.Pointer[recorder]

// Labels 只允许 model/provider/stream 与必要的 outcome；禁止 ID 与正文。
type Labels struct {
	Model    string
	Provider string
	Stream   bool
	Outcome  string
}

// Configure 将 LLM 业务指标注册到仅供 Prometheus /metrics 抓取的 MeterProvider；不向 Logfire 导出。
func Configure(meter otelmetric.Meter) error {
	requests, err := meter.Int64Counter(
		"wg.modelhub.llm.requests",
		otelmetric.WithDescription("Accepted text LLM requests before provider call"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.requests: %w", err)
	}
	terminals, err := meter.Int64Counter(
		"wg.modelhub.llm.terminals",
		otelmetric.WithDescription("Accepted text LLM terminal outcomes"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.terminals: %w", err)
	}
	ttft, err := meter.Float64Histogram(
		"wg.modelhub.llm.ttft",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Streaming TTFT from Generate receipt to first non-empty text or tool name"),
		otelmetric.WithExplicitBucketBoundaries(ttftBuckets...),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.ttft: %w", err)
	}
	duration, err := meter.Float64Histogram(
		"wg.modelhub.llm.duration",
		otelmetric.WithUnit("s"),
		otelmetric.WithDescription("Accepted text LLM duration from Generate receipt to return"),
		otelmetric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.duration: %w", err)
	}
	inputTokens, err := meter.Int64Counter(
		"wg.modelhub.llm.input.tokens",
		otelmetric.WithDescription("LLM input tokens from provider usage"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.input.tokens: %w", err)
	}
	outputTokens, err := meter.Int64Counter(
		"wg.modelhub.llm.output.tokens",
		otelmetric.WithDescription("LLM output tokens from provider usage"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.output.tokens: %w", err)
	}
	// 默认 UnderscoreEscaping 会把 instrument 名里的 total_tokens 收成 tokens_total；
	// 用 tokens.reported 导出为 wg_modelhub_llm_tokens_reported_total，表示供应商 usage.total。
	reportedTokens, err := meter.Int64Counter(
		"wg.modelhub.llm.tokens.reported",
		otelmetric.WithDescription("LLM provider-reported total tokens from usage"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.tokens.reported: %w", err)
	}
	cachedTokens, err := meter.Int64Counter(
		"wg.modelhub.llm.cached.tokens",
		otelmetric.WithDescription("LLM cached tokens (subset of input) from provider usage"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.cached.tokens: %w", err)
	}
	reasoningTokens, err := meter.Int64Counter(
		"wg.modelhub.llm.reasoning.tokens",
		otelmetric.WithDescription("LLM reasoning tokens (subset of output) from provider usage"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.reasoning.tokens: %w", err)
	}
	usageReports, err := meter.Int64Counter(
		"wg.modelhub.llm.usage.reports",
		otelmetric.WithDescription("Whether provider usage was present on successful terminal"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.usage.reports: %w", err)
	}
	cacheRequests, err := meter.Int64Counter(
		"wg.modelhub.llm.cache.requests",
		otelmetric.WithDescription("Cache classification for successful text terminals"),
	)
	if err != nil {
		return fmt.Errorf("create wg.modelhub.llm.cache.requests: %w", err)
	}
	current.Store(&recorder{
		requests:        requests,
		terminals:       terminals,
		ttft:            ttft,
		duration:        duration,
		inputTokens:     inputTokens,
		outputTokens:    outputTokens,
		reportedTokens:  reportedTokens,
		cachedTokens:    cachedTokens,
		reasoningTokens: reasoningTokens,
		usageReports:    usageReports,
		cacheRequests:   cacheRequests,
	})
	return nil
}

func baseAttrs(l Labels) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("model", l.Model),
		attribute.String("provider", l.Provider),
		// Prometheus 标签由 OTel bool 导出为 true/false，与看板 stream 变量一致。
		attribute.Bool("stream", l.Stream),
	}
}

// RecordRequest 在文本能力路由成功、真实供应商调用前计一次。
func RecordRequest(ctx context.Context, l Labels) {
	r := current.Load()
	if r == nil {
		return
	}
	r.requests.Add(ctx, 1, otelmetric.WithAttributes(baseAttrs(l)...))
}

// RecordTerminal 记录已接纳文本请求的 outcome 与总耗时。
func RecordTerminal(ctx context.Context, l Labels, totalDuration time.Duration) {
	r := current.Load()
	if r == nil {
		return
	}
	attrs := append(baseAttrs(l), attribute.String("outcome", l.Outcome))
	r.terminals.Add(ctx, 1, otelmetric.WithAttributes(attrs...))
	r.duration.Record(ctx, totalDuration.Seconds(), otelmetric.WithAttributes(attrs...))
}

// RecordTTFT 仅流式请求在首个非空 text / tool name 时记录一次。
func RecordTTFT(ctx context.Context, l Labels, ttft time.Duration) {
	r := current.Load()
	if r == nil || !l.Stream {
		return
	}
	r.ttft.Record(ctx, ttft.Seconds(), otelmetric.WithAttributes(baseAttrs(l)...))
}

// RecordUsageAndCache 在供应商成功终态后、最终 Send 前记录；usage 缺失只记 coverage，不造 Token 0。
func RecordUsageAndCache(ctx context.Context, l Labels, cachingMode string, usage *modelhubv2.Usage) {
	r := current.Load()
	if r == nil {
		return
	}
	base := baseAttrs(l)
	present := usage != nil
	state := UsageMissing
	if present {
		state = UsagePresent
	}
	r.usageReports.Add(ctx, 1, otelmetric.WithAttributes(append(append([]attribute.KeyValue{}, base...), attribute.String("state", state))...))

	result := ClassifyCache(cachingMode, usage)
	r.cacheRequests.Add(ctx, 1, otelmetric.WithAttributes(append(append([]attribute.KeyValue{}, base...), attribute.String("result", result))...))

	if !present {
		return
	}
	r.inputTokens.Add(ctx, usage.GetInputTokens(), otelmetric.WithAttributes(base...))
	r.outputTokens.Add(ctx, usage.GetOutputTokens(), otelmetric.WithAttributes(base...))
	r.reportedTokens.Add(ctx, usage.GetTotalTokens(), otelmetric.WithAttributes(base...))
	r.cachedTokens.Add(ctx, usage.GetCachedTokens(), otelmetric.WithAttributes(base...))
	r.reasoningTokens.Add(ctx, usage.GetReasoningTokens(), otelmetric.WithAttributes(base...))
}

// ClassifyCache：hit 只看 cached_tokens>0；explicit_disabled 且非 hit 才是 disabled；Ark implicit 不得标 disabled。
func ClassifyCache(cachingMode string, usage *modelhubv2.Usage) string {
	if usage != nil && usage.GetCachedTokens() > 0 {
		return CacheHit
	}
	if cachingMode == CachingModeExplicitDisabled {
		return CacheDisabled
	}
	if usage != nil {
		return CacheMiss
	}
	return CacheUnknown
}

// MapOutcome：Canceled / DeadlineExceeded 归 cancelled，其余非 nil 错误归 failed。
func MapOutcome(err error) string {
	if err == nil {
		return OutcomeSucceeded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return OutcomeCancelled
	}
	switch status.Code(err) {
	case codes.Canceled, codes.DeadlineExceeded:
		return OutcomeCancelled
	default:
		return OutcomeFailed
	}
}

// IsFirstModelOutput 判定是否出现首个非空 text delta 或首个非空 tool-call name。
func IsFirstModelOutput(event *modelhubv2.GenerateEvent) bool {
	for _, item := range event.GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.OutputItem_Text:
			if strings.TrimSpace(value.Text) != "" {
				return true
			}
		case *modelhubv2.OutputItem_ToolCall:
			if value.ToolCall != nil && strings.TrimSpace(value.ToolCall.GetName()) != "" {
				return true
			}
		}
	}
	return false
}
