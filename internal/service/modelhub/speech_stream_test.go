package modelhub

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/elevenlabstts"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func speechStreamClient(t *testing.T, handler http.HandlerFunc) modelhubv2.ModelHubServiceClient {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	p, err := elevenlabstts.New(elevenlabstts.Config{Name: "eleven", APIKey: "test", VoiceID: "voice", BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	svc := newTestService(config.Config{Providers: map[string]config.ProviderConfig{
		"eleven": {Models: []string{models.ElevenFlashV25}, ElevenLabsTTS: &config.ElevenLabsTTSProviderConfig{APIKey: "test", VoiceID: "voice"}},
	}}, map[string]provider.Set{"eleven": {Speech: p}}, nil)
	lis := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	modelhubv2.RegisterModelHubServiceServer(server, svc)
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///speech-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return modelhubv2.NewModelHubServiceClient(conn)
}

// 上游必须等客户端收到首包才继续，证明 HTTP→service→gRPC 没有整句缓存。
func TestSpeechStreamDeliversBeforeUpstreamEOF(t *testing.T) {
	release := make(chan struct{})
	client := speechStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/text-to-speech/voice/stream" {
			t.Errorf("path=%s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Write([]byte("last"))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.SynthesizeSpeechStream(ctx, &modelhubv2.SynthesizeSpeechRequest{Model: models.ElevenFlashV25, Text: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if string(first.GetAudio().GetData()) != "first" {
		t.Fatal(first)
	}
	close(release)
	var tail []byte
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		tail = append(tail, chunk.GetAudio().GetData()...)
	}
	if string(tail) != "last" {
		t.Fatalf("tail=%q", tail)
	}
}

func TestSpeechStreamCancelClosesUpstream(t *testing.T) {
	closed := make(chan struct{})
	client := speechStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.SynthesizeSpeechStream(ctx, &modelhubv2.SynthesizeSpeechRequest{Model: models.ElevenFlashV25, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("upstream not canceled")
	}
	if _, err = stream.Recv(); err == nil || err == io.EOF {
		t.Fatalf("cancel reported success: %v", err)
	}
}

func TestSpeechStreamTruncatedBodyFailsAfterAudio(t *testing.T) {
	client := speechStreamClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.SynthesizeSpeechStream(ctx, &modelhubv2.SynthesizeSpeechRequest{Model: models.ElevenFlashV25, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || string(first.GetAudio().GetData()) != "partial" {
		t.Fatalf("first=%v err=%v", first, err)
	}
	if _, err = stream.Recv(); err == nil || err == io.EOF {
		t.Fatalf("truncated audio reported success: %v", err)
	}
}

func TestSpeechStreamEmptyOrProviderErrorIsNotSuccess(t *testing.T) {
	for _, code := range []int{200, 429} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			client := speechStreamClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, err := client.SynthesizeSpeechStream(ctx, &modelhubv2.SynthesizeSpeechRequest{Model: models.ElevenFlashV25, Text: "hello"})
			if err == nil {
				_, err = stream.Recv()
			}
			if err == nil || err == io.EOF {
				t.Fatalf("status %d reported success: %v", code, err)
			}
		})
	}
}
