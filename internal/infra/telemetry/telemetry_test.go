package telemetry

import (
	"context"
	"errors"
	"testing"

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
