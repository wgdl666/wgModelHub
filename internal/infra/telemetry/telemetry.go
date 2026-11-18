package telemetry

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/asrmetric"
	"github.com/wgdl666/wgModelHub/internal/infra/channelmetric"
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
	channels       *channelmetric.Publisher
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

	// Prometheus exporter 已 WithoutTargetInfo：metrics resource 不会进入业务指标。
	// LLM 指标走 MeterProvider；渠道信息直接挂在同一个 Registry，随 /metrics 抓取。
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
	if err := asrmetric.Configure(metricProvider.Meter(serviceName)); err != nil {
		_ = metricProvider.Shutdown(ctx)
		_ = shared.Shutdown(ctx)
		return nil, fmt.Errorf("configure asr metrics: %w", err)
	}
	channels, err := channelmetric.New(promReader.Registerer())
	if err != nil {
		_ = metricProvider.Shutdown(ctx)
		_ = shared.Shutdown(ctx)
		return nil, err
	}

	return &Runtime{
		shared:         shared,
		metricProvider: metricProvider,
		metricsHandler: promReader.Handler(),
		channels:       channels,
	}, nil
}

// PublishChannels 把当前实例的渠道写进 /metrics。配置热更新后应再调用一次。
// Setup 成功时 channels 已由 channelmetric.New 装配，调用方拿到的就是这个对象。
func (r *Runtime) PublishChannels(providers map[string]config.ProviderConfig) error {
	return r.channels.Publish(providers)
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

// TraceID 返回当前 span 的 trace。账本和薇光点亮出站表单都走这里，避免两处各解析一次对不上。
// 无效 span 返回空字符串；调用方不得把空值下发成请求参数。
func TraceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// RecordError 把真实失败记到当前 span。上游主动取消（如 wardrobe VTON 三路竞速淘汰）
// 是正常中断：仅在本地 context 也已取消时降级，避免隐藏远端异常 Canceled。
// 账本/指标仍保留 cancelled；真实超时和 429 继续标错。
func RecordError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	span := trace.SpanFromContext(ctx)
	if errors.Is(ctx.Err(), context.Canceled) && isCallerCanceled(err) {
		span.SetAttributes(attribute.String("outcome", "cancelled"))
		// HTTP、服务层会重复观察同一次取消；span 已保留终态，不逐层打印 Warning。
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
	started := time.Now()
	span.SetAttributes(
		attribute.String("http.request.method", request.Method),
		attribute.String("server.address", request.URL.Host),
	)
	// 同一上游故障需要区分 DNS、连接、首包与读正文；仅记阶段时间和连接元数据。
	stage := func(name string, attrs ...attribute.KeyValue) {
		attrs = append(attrs, attribute.Float64("elapsed_ms", float64(time.Since(started).Microseconds())/1000))
		span.AddEvent(name, trace.WithAttributes(attrs...))
	}
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { stage("http.dns.start") },
		DNSDone:      func(info httptrace.DNSDoneInfo) { stage("http.dns.done", attribute.Bool("failed", info.Err != nil)) },
		ConnectStart: func(network, _ string) { stage("http.connect.start", attribute.String("network", network)) },
		ConnectDone: func(network, _ string, err error) {
			stage("http.connect.done", attribute.String("network", network), attribute.Bool("failed", err != nil))
		},
		TLSHandshakeStart: func() { stage("http.tls.start") },
		TLSHandshakeDone:  func(_ tls.ConnectionState, err error) { stage("http.tls.done", attribute.Bool("failed", err != nil)) },
		GotConn: func(info httptrace.GotConnInfo) {
			stage("http.connection.acquired", attribute.Bool("reused", info.Reused), attribute.Bool("was_idle", info.WasIdle))
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			stage("http.request.written", attribute.Bool("failed", info.Err != nil))
		},
		GotFirstResponseByte: func() { stage("http.response.first_byte") },
	})
	clone := request.Clone(ctx)
	clone.Header = request.Header.Clone()
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(clone.Header))
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		span.SetAttributes(attribute.String("error.phase", "round_trip"))
		RecordError(ctx, err)
		span.End()
		return nil, err
	}
	span.SetAttributes(attribute.Float64("http.response.headers_elapsed_ms", float64(time.Since(started).Microseconds())/1000))
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.StatusCode >= 400 {
		span.SetStatus(otelcodes.Error, fmt.Sprintf("HTTP %d", response.StatusCode))
	}
	if response.Body == nil || response.Body == http.NoBody {
		span.End()
		return response, nil
	}
	response.Body = &traceResponseBody{body: response.Body, span: span, ctx: ctx, headersAt: time.Now()}
	return response, nil
}

type traceResponseBody struct {
	body      io.ReadCloser
	span      trace.Span
	ctx       context.Context
	headersAt time.Time
	once      sync.Once
	bytes     int64
}

func (b *traceResponseBody) Read(target []byte) (int, error) {
	n, err := b.body.Read(target)
	b.bytes += int64(n)
	if err != nil {
		// HTTP 200 只表示响应头已到，读正文超时仍必须在同一出站 Span 上可见。
		if !errors.Is(err, io.EOF) {
			b.span.SetAttributes(attribute.String("error.phase", "read_body"))
			RecordError(b.ctx, err)
		}
		b.finish()
	}
	return n, err
}

func (b *traceResponseBody) Close() error {
	err := b.body.Close()
	if err != nil {
		b.span.SetAttributes(attribute.String("error.phase", "close_body"))
		RecordError(b.ctx, err)
	}
	b.finish()
	return err
}

func (b *traceResponseBody) finish() {
	b.once.Do(func() {
		b.span.SetAttributes(attribute.Int64("http.response.body.size", b.bytes))
		b.span.SetAttributes(attribute.Float64("http.response.body.duration_ms", float64(time.Since(b.headersAt).Microseconds())/1000))
		b.span.End()
	})
}
