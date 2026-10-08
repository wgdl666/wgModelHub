package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	grpcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRecordErrorCallerCanceledDoesNotMarkSpanError(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "context_canceled", err: context.Canceled},
		{name: "grpc_canceled", err: status.Error(grpcodes.Canceled, "request canceled")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			ctx, span := tp.Tracer("test").Start(context.Background(), "modelhub.Generate")

			RecordError(ctx, tc.err)
			span.End()

			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("ended spans=%d", len(spans))
			}
			got := spans[0]
			if got.Status().Code != codes.Unset {
				t.Fatalf("status=%v want Unset", got.Status().Code)
			}
			if len(got.Events()) != 0 {
				t.Fatalf("exception events=%d want 0", len(got.Events()))
			}
			found := false
			for _, attr := range got.Attributes() {
				if string(attr.Key) == "outcome" && attr.Value.AsString() == "cancelled" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing outcome=cancelled attrs=%v", got.Attributes())
			}
		})
	}
}

func TestRecordErrorRealFailureStillMarksSpanError(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := tp.Tracer("test").Start(context.Background(), "modelhub.Generate")

	RecordError(ctx, errors.New("provider boom"))
	span.End()

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans=%d", len(spans))
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("status=%v want Error", spans[0].Status().Code)
	}
}

func TestIsCallerCanceled(t *testing.T) {
	if !isCallerCanceled(context.Canceled) {
		t.Fatal("context.Canceled")
	}
	if !isCallerCanceled(status.Error(grpcodes.Canceled, "request canceled")) {
		t.Fatal("grpc Canceled")
	}
	if isCallerCanceled(context.DeadlineExceeded) {
		t.Fatal("DeadlineExceeded must stay a real error signal")
	}
	if isCallerCanceled(errors.New("boom")) {
		t.Fatal("generic error")
	}
}

// TestNewTimedHTTPClientEmitsProviderHTTPSpan 自测：换 traced client 后能看到主机与状态码，且 Timeout 不丢。
func TestNewTimedHTTPClientEmitsProviderHTTPSpan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
	}))
	t.Cleanup(server.Close)

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	client := NewTimedHTTPClient(20 * time.Second)
	if client.Timeout != 20*time.Second {
		t.Fatalf("timeout=%v want 20s", client.Timeout)
	}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTooManyRequests || string(body) != `{"error":"rate_limited"}` {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	var httpSpan sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "provider.http" {
			httpSpan = span
			break
		}
	}
	if httpSpan == nil {
		t.Fatal("missing provider.http span")
	}
	attrs := map[string]any{}
	for _, attr := range httpSpan.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsInterface()
	}
	if attrs["http.request.method"] != "GET" {
		t.Fatalf("method=%v", attrs["http.request.method"])
	}
	if attrs["server.address"] != server.Listener.Addr().String() {
		t.Fatalf("server.address=%v want %s", attrs["server.address"], server.Listener.Addr().String())
	}
	if attrs["http.response.status_code"] != int64(429) {
		t.Fatalf("status_code=%v want 429", attrs["http.response.status_code"])
	}
	if httpSpan.Status().Code != codes.Error {
		t.Fatalf("span status=%v want Error for HTTP 429", httpSpan.Status().Code)
	}
}
