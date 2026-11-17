package modelhub

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type deadlineEOFText struct{ usageText }

func (*deadlineEOFText) GenerateStream(ctx context.Context, _ string, _ *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	if err := emit(provider.TextDeltaEvent("partial")); err != nil {
		return nil, err
	}
	<-ctx.Done()
	// 复现线上供应商读取在截止时结束、最后一次客户端 Send 才暴露传输失败的路径。
	return provider.MetadataFinalEvent("", "", nil), nil
}

func TestGenerateTextHTTP2DeadlineCountsFailed(t *testing.T) {
	scrape := installMetricScraper(t)
	service := newTestService(config.Config{Providers: map[string]config.ProviderConfig{
		"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
	}}, map[string]provider.Set{"ark": {Text: &deadlineEOFText{}}}, nil)
	done := make(chan struct{})
	server := grpc.NewServer(grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		defer close(done)
		return handler(srv, stream)
	}))
	modelhubv2.RegisterModelHubServiceServer(server, service)
	listener := bufconn.Listen(1 << 20)
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	transport := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
		return listener.DialContext(ctx)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	request := textRequest("chat-model", "do")
	request.Output.Stream = true
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	frame = append(frame, payload...)
	httpRequest, err := http.NewRequest(http.MethodPost, "http://bufnet/wg_model_hub.v2.ModelHubService/Generate", bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("content-type", "application/grpc")
	httpRequest.Header.Set("te", "trailers")
	// 只让服务端 gRPC 定时器触发截止，避免客户端取消先到而混淆验收原因。
	httpRequest.Header.Set("grpc-timeout", "100m")
	response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not finish after deadline")
	}
	body := scrape()
	if metricSum(t, body, "wg_modelhub_llm_terminals_total") != 1 || metricValue(t, body, "wg_modelhub_llm_terminals_total", map[string]string{"outcome": "failed"}) != 1 {
		t.Fatalf("transport deadline must produce one failed terminal\n%s", body)
	}
}
