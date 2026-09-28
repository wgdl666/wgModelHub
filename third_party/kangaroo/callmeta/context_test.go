package callmeta

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
)

func TestPersistRestoreAndIsolation(t *testing.T) {
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
	original, cancel := context.WithCancel(WithBusiness(trace.ContextWithSpanContext(context.Background(), parent), PromptDebug, "a"))
	member, _ := baggage.NewMember("secret", "must-not-propagate")
	bag, _ := baggage.New(member)
	original = baggage.ContextWithBaggage(original, bag)
	task := map[string]any{}
	SaveTask(original, task)
	cancel()
	raw, _ := json.Marshal(task)
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	restored := RestoreTask(context.Background(), persisted)
	if restored.Err() != nil {
		t.Fatal("inherited cancellation")
	}
	if trace.SpanContextFromContext(restored).TraceID() != parent.TraceID() {
		t.Fatal("lost trace")
	}
	if key, id := Business(restored); key != PromptDebug || id != "a" {
		t.Fatalf("lost business %s %s", key, id)
	}
	if len(Capture(restored)) != 3 {
		t.Fatalf("unexpected metadata: %v", Capture(restored))
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i)
			ctx := WithBusiness(restored, PromptDebug, id)
			if _, got := Business(Restore(context.Background(), Capture(ctx))); got != id {
				t.Errorf("cross request %s", got)
			}
		}(i)
	}
	wg.Wait()
	if _, id := Business(restored); id != "a" {
		t.Fatal("mutated parent")
	}
	if _, id := Business(Restore(restored, nil)); id != "" {
		t.Fatal("worker inherited previous business")
	}
}

type healthServer struct {
	healthpb.UnimplementedHealthServer
	received chan map[string]string
}

func (s *healthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	s.received <- Capture(ctx)
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}
func (s *healthServer) Watch(_ *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	s.received <- Capture(stream.Context())
	return stream.Send(&healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING})
}

func TestRealGRPCUnaryAndStream(t *testing.T) {
	previous := otel.GetTracerProvider()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = tp.Shutdown(context.Background()) })
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.StatsHandler(GRPCServerHandler()))
	service := &healthServer{received: make(chan map[string]string, 4)}
	healthpb.RegisterHealthServer(server, service)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	client, err := grpc.NewClient("passthrough:///test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithStatsHandler(GRPCClientHandler()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	ctx, span := tp.Tracer("test").Start(context.Background(), "origin")
	defer span.End()
	ctx = WithBusiness(ctx, PromptDebug, "snapshot")
	api := healthpb.NewHealthClient(client)
	if _, err = api.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	stream, err := api.Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		md := <-service.received
		got := Restore(context.Background(), md)
		if _, id := Business(got); id != "snapshot" {
			t.Fatal(md)
		}
		if trace.SpanContextFromContext(got).TraceID() != span.SpanContext().TraceID() {
			t.Fatal("trace split")
		}
		if trace.SpanContextFromContext(got).SpanID() == span.SpanContext().SpanID() {
			t.Fatal("server reused origin span")
		}
	}
	if _, err = api.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatal(err)
	}
	if md := <-service.received; md[BusinessID] != "" {
		t.Fatal("normal request inherited draft")
	}
}

func TestHTTPToTaskToGRPCMetadataContract(t *testing.T) {
	ctx := WithBusiness(context.Background(), PromptDebug, "snapshot-http")
	headers := http.Header{}
	Propagator{}.Inject(ctx, HTTPHeaders(headers))
	if headers.Get("X-WG-Business-Key") != PromptDebug || headers.Get(BusinessKey) != "" {
		t.Fatalf("nonstandard HTTP headers: %v", headers)
	}
	received := Propagator{}.Extract(context.Background(), HTTPHeaders(headers))
	task := map[string]any{}
	SaveTask(received, task)
	body, _ := json.Marshal(task)
	var persisted map[string]any
	_ = json.Unmarshal(body, &persisted)
	md := Capture(RestoreTask(context.Background(), persisted))
	if md[BusinessKey] != PromptDebug || md[BusinessID] != "snapshot-http" {
		t.Fatal(md)
	}
}
