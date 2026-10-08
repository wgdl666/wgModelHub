package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
	"github.com/wgdl666/wgModelHub/internal/infra/metricserver"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const logfireEndpoint = "logfire-us.pydantic.dev"

// Runtime 持有 Logfire 日志/Trace，以及仅供 Prometheus 抓取的 LLM MeterProvider。
type Runtime struct {
	shared         *logs.Runtime
	metricProvider *sdkmetric.MeterProvider
	metricsHandler http.Handler
}

// Setup 在 Nacos 配置加载后装配；日志/Trace 仍走 Logfire，业务 LLM 指标只进 Prometheus。
func Setup(ctx context.Context, cfg config.LogfireConfig) (*Runtime, error) {
	token := strings.TrimSpace(cfg.Token)
	endpoint := ""
	headers := map[string]string{}
	if token != "" {
		endpoint = logfireEndpoint
		headers["Authorization"] = token
	}
	serviceName := strings.TrimSpace(cfg.Service)
	if serviceName == "" {
		serviceName = "wg-model-hub"
	}
	shared, err := logs.Setup(ctx, logs.Config{
		ServiceName:    serviceName,
		ServiceVersion: strings.TrimSpace(cfg.Version),
		Environment:    strings.TrimSpace(cfg.Env),
		Endpoint:       endpoint,
		Headers:        headers,
		MinLevel:       strings.TrimSpace(cfg.OtelLogLevel),
		Console:        true,
	})
	if err != nil {
		return nil, err
	}

	// Prometheus exporter 已 WithoutTargetInfo：metrics resource 不会进入业务指标，
	// 也无其它消费者；只保留独立 MeterProvider + Reader，避免无效 resource 构造。
	promReader, err := metricserver.NewReader()
	if err != nil {
		_ = shared.Shutdown(ctx)
		return nil, err
	}
	metricProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(promReader.ManualReader()),
	)
	if err := llmmetric.Configure(metricProvider.Meter(serviceName)); err != nil {
		_ = metricProvider.Shutdown(ctx)
		_ = shared.Shutdown(ctx)
		return nil, fmt.Errorf("configure llm metrics: %w", err)
	}

	return &Runtime{
		shared:         shared,
		metricProvider: metricProvider,
		metricsHandler: promReader.Handler(),
	}, nil
}

// MetricsHandler 返回挂到内部 HTTP 端口 /metrics 的 Prometheus 抓取入口。
// Setup 成功返回时 metricsHandler 已成立，不做重复判空。
func (r *Runtime) MetricsHandler() http.Handler {
	return r.metricsHandler
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	// 同时关闭 Logfire（trace/log）与 Prometheus MeterProvider，保留两侧错误。
	return errors.Join(r.shared.Shutdown(ctx), r.metricProvider.Shutdown(ctx))
}

func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer("github.com/wgdl666/wgModelHub").Start(ctx, name)
}

// RecordError 把真实失败记到当前 span。上游主动取消（如 wardrobe VTON 三路竞速淘汰）
// 是正常中断：账本/指标已归 cancelled，这里不得抬成 Error，否则 Logfire/Ops 排障会被噪声淹没。
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if isCallerCanceled(err) {
		span.SetAttributes(attribute.String("outcome", "cancelled"))
		logs.Default().With("error", err.Error()).CtxWarn(ctx, "modelhub_request_canceled")
		return
	}
	span.RecordError(err)
	span.SetStatus(otelcodes.Error, err.Error())
}

// isCallerCanceled 认 context.Canceled 与已收敛的 gRPC codes.Canceled（如 provider.ToStatus）。
func isCallerCanceled(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Canceled {
		return true
	}
	return false
}

// NewHTTPClient 只记录目标主机、状态码和正文大小，禁止采集请求或响应正文。
func NewHTTPClient() *http.Client {
	return NewHTTPClientWithTransport(http.DefaultTransport)
}

// NewTimedHTTPClient 在 provider.http 观测之上钉死超时。
// 裸 &http.Client{Timeout} 换成 traced client 时必须走这里，否则会丢掉 Timeout、把排障所需的主机/状态码也一起丢掉。
func NewTimedHTTPClient(timeout time.Duration) *http.Client {
	client := NewHTTPClient()
	client.Timeout = timeout
	return client
}

func NewHTTPClientWithTransport(base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Transport: &traceTransport{base: base}}
}

type traceTransport struct {
	base http.RoundTripper
}

func (t *traceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, span := StartSpan(request.Context(), "provider.http")
	span.SetAttributes(
		attribute.String("http.request.method", request.Method),
		attribute.String("server.address", request.URL.Host),
	)
	clone := request.Clone(ctx)
	clone.Header = request.Header.Clone()
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(clone.Header))
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		RecordError(ctx, err)
		span.End()
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.StatusCode >= 400 {
		span.SetStatus(otelcodes.Error, fmt.Sprintf("HTTP %d", response.StatusCode))
	}
	if response.Body == nil || response.Body == http.NoBody {
		span.End()
		return response, nil
	}
	response.Body = &traceResponseBody{body: response.Body, span: span}
	return response, nil
}

type traceResponseBody struct {
	body  io.ReadCloser
	span  trace.Span
	once  sync.Once
	bytes int64
}

func (b *traceResponseBody) Read(target []byte) (int, error) {
	n, err := b.body.Read(target)
	b.bytes += int64(n)
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *traceResponseBody) Close() error {
	err := b.body.Close()
	b.finish()
	return err
}

func (b *traceResponseBody) finish() {
	b.once.Do(func() {
		b.span.SetAttributes(attribute.Int64("http.response.body.size", b.bytes))
		b.span.End()
	})
}
