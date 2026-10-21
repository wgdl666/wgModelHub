package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// 真实 HTTP 交换必须保留调用方原有 trace hook，同时给慢请求留下连接与首包边界。
func TestHTTPStagesComposeWithCallerTrace(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(server.Close)
	callerHook := false
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotFirstResponseByte: func() { callerHook = true },
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	resp, err := NewHTTPClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !callerHook {
		t.Fatal("caller HTTP trace was replaced")
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans=%d", len(spans))
	}
	events := map[string]bool{}
	for _, event := range spans[0].Events() {
		events[event.Name] = true
	}
	for _, name := range []string{"http.connect.start", "http.connect.done", "http.connection.acquired", "http.request.written", "http.response.first_byte"} {
		if !events[name] {
			t.Fatalf("missing stage %s", name)
		}
	}
}

type brokenResponseBody struct{}

func (brokenResponseBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (brokenResponseBody) Close() error             { return nil }

type successfulHeadersTransport struct{}

func (successfulHeadersTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: brokenResponseBody{}}, nil
}

// 曾经 HTTP 200 后读正文超时仍留下成功 Span；供应商失败必须在这个出站 Span 可见。
func TestHTTPBodyDeadlineDoesNotLookSuccessful(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })
	response, err := NewHTTPClientWithTransport(successfulHeadersTransport{}).Get("https://example.invalid/embedding?secret=must-not-be-traced")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(response.Body)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read error=%v", err)
	}
	_ = response.Body.Close()
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Status().Code != codes.Error {
		t.Fatalf("body timeout did not mark error: %v", spans)
	}
	phase := ""
	for _, attr := range spans[0].Attributes() {
		if string(attr.Key) == "error.phase" {
			phase = attr.Value.AsString()
		}
		if strings.Contains(attr.Value.Emit(), "must-not-be-traced") {
			t.Fatal("request query leaked into trace")
		}
	}
	if phase != "read_body" {
		t.Fatalf("phase=%s", phase)
	}
}
