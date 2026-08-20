package telemetry

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const logfireEndpoint = "logfire-us.pydantic.dev"

type Runtime struct {
	shared *logs.Runtime
}

// Setup 在 Nacos 配置加载后装配；token/env 来自本服务 Data ID，不再读 LOGFIRE_* 环境变量。
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
	return &Runtime{shared: shared}, nil
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.shared == nil {
		return nil
	}
	return r.shared.Shutdown(ctx)
}

func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer("github.com/wgdl666/wgModelHub").Start(ctx, name)
}

func RecordError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// NewHTTPClient 只记录目标主机、状态码和正文大小，禁止采集请求或响应正文。
func NewHTTPClient() *http.Client {
	return NewHTTPClientWithTransport(http.DefaultTransport)
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
