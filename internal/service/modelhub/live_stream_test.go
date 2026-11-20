package modelhub

import (
	"context"
	"github.com/wgdl666/wgModelHub/config"
	v2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"sync"
	"testing"
	"time"
)

type mockLive struct {
	ctx    context.Context
	sent   chan *v2.LiveClientMessage
	closed chan struct{}
	once   sync.Once
}

func (m *mockLive) OpenLive(ctx context.Context, _ string, _ *v2.LiveStart) (provider.LiveSession, error) {
	m.ctx = ctx
	return m, nil
}
func (m *mockLive) Send(v *v2.LiveClientMessage) error { m.sent <- v; return nil }
func (m *mockLive) Receive() (*v2.LiveServerMessage, error) {
	select {
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	case <-m.sent:
		return &v2.LiveServerMessage{InputTranscript: &v2.LiveTranscript{Text: "粉色裙子", Final: true}}, nil
	}
}
func (m *mockLive) Close() error { m.once.Do(func() { close(m.closed) }); return nil }
func TestLiveStreamRoutesAndClosesOnCancellation(t *testing.T) {
	m := &mockLive{sent: make(chan *v2.LiveClientMessage, 1), closed: make(chan struct{})}
	cfg := config.Config{Providers: map[string]config.ProviderConfig{"gemini": {Models: []string{models.Gemini38Live}, Gemini: &config.GeminiProviderConfig{APIKey: "test"}}}}
	server := grpc.NewServer()
	v2.RegisterModelHubServiceServer(server, newTestService(cfg, map[string]provider.Set{"gemini": {Live: m}}, nil))
	lis := bufconn.Listen(1 << 20)
	go server.Serve(lis)
	defer server.Stop()
	conn, e := grpc.NewClient("passthrough:///live", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stream, e := v2.NewModelHubServiceClient(conn).LiveStream(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, msg := range []*v2.LiveClientMessage{{Item: &v2.LiveClientMessage_Start{Start: &v2.LiveStart{Model: models.Gemini38Live, Input: &v2.Input{}}}}, {Item: &v2.LiveClientMessage_Pcm{Pcm: []byte{0, 0}}}} {
		if e = stream.Send(msg); e != nil {
			t.Fatal(e)
		}
	}
	event, e := stream.Recv()
	if e != nil || event.GetInputTranscript().GetText() != "粉色裙子" {
		t.Fatalf("event=%v err=%v", event, e)
	}
	cancel()
	select {
	case <-m.closed:
	case <-time.After(time.Second):
		t.Fatal("Live session leaked after cancellation")
	}
}
