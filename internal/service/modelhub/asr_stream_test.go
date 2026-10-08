package modelhub

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type mockASRProvider struct {
	mu      sync.Mutex
	model   string
	start   *modelhubv2.TranscribeSpeechStart
	session *mockASRSession
}

func (p *mockASRProvider) OpenASR(_ context.Context, model string, start *modelhubv2.TranscribeSpeechStart, emit provider.ASREmit) (provider.ASRSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.model, p.start = model, start
	p.session = &mockASRSession{id: "mock-session", emit: emit, errs: make(chan error)}
	return p.session, nil
}

type mockASRSession struct {
	id        string
	emit      provider.ASREmit
	errs      chan error
	audio     []byte
	finalized bool
	stopped   bool
}

func (s *mockASRSession) ID() string           { return s.id }
func (s *mockASRSession) Errors() <-chan error { return s.errs }
func (s *mockASRSession) SendAudio(data []byte) error {
	s.audio = append(s.audio, data...)
	return s.emit(&modelhubv2.TranscribeSpeechTranscript{Text: "你好", IsFinal: true})
}
func (s *mockASRSession) Finalize() error { s.finalized = true; return nil }
func (s *mockASRSession) Stop() error     { s.stopped = true; return nil }

func asrStreamClient(t *testing.T, asr provider.ASRProvider) modelhubv2.ModelHubServiceClient {
	t.Helper()
	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"mock_asr": {
			Models:    []string{models.SonioxSTTRTV5},
			SonioxASR: &config.SonioxASRProviderConfig{APIKey: "test"},
		},
	}}
	svc := newTestService(cfg, map[string]provider.Set{"mock_asr": {ASR: asr}}, nil)
	lis := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	modelhubv2.RegisterModelHubServiceServer(server, svc)
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///asr-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return modelhubv2.NewModelHubServiceClient(conn)
}

func validASRStart() *modelhubv2.TranscribeSpeechStart {
	return &modelhubv2.TranscribeSpeechStart{
		Model:        models.SonioxSTTRTV5,
		Encoding:     modelhubv2.AudioEncoding_AUDIO_ENCODING_PCM_S16LE,
		SampleRateHz: 16000,
		Channels:     1,
	}
}

// mock 会话覆盖 start→session→audio→transcript→finalize→stop 的完整双向桥接与真实模型路由。
func TestTranscribeSpeechStreamRoutesAndBridges(t *testing.T) {
	mock := &mockASRProvider{}
	client := asrStreamClient(t, mock)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.TranscribeSpeechStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{
		Item: &modelhubv2.TranscribeSpeechClientMessage_Start{Start: validASRStart()},
	}); err != nil {
		t.Fatal(err)
	}
	session, err := stream.Recv()
	if err != nil || session.GetSession().GetSessionId() != "mock-session" {
		t.Fatalf("session=%v err=%v", session, err)
	}
	if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{
		Item: &modelhubv2.TranscribeSpeechClientMessage_Audio{Audio: &modelhubv2.TranscribeSpeechAudio{Data: []byte{1, 2}}},
	}); err != nil {
		t.Fatal(err)
	}
	transcript, err := stream.Recv()
	if err != nil || transcript.GetTranscript().GetText() != "你好" || !transcript.GetTranscript().GetIsFinal() {
		t.Fatalf("transcript=%v err=%v", transcript, err)
	}
	_ = stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Finalize{Finalize: &modelhubv2.TranscribeSpeechFinalize{}}})
	_ = stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Stop{Stop: &modelhubv2.TranscribeSpeechStop{}}})
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	if mock.model != models.SonioxSTTRTV5 || string(mock.session.audio) != "\x01\x02" || !mock.session.finalized || !mock.session.stopped {
		t.Fatalf("model=%q session=%+v", mock.model, mock.session)
	}
}

func TestTranscribeSpeechStreamRejectsIllegalFirstMessage(t *testing.T) {
	client := asrStreamClient(t, &mockASRProvider{})
	stream, err := client.TranscribeSpeechStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&modelhubv2.TranscribeSpeechClientMessage{
		Item: &modelhubv2.TranscribeSpeechClientMessage_Audio{Audio: &modelhubv2.TranscribeSpeechAudio{Data: []byte{1}}},
	})
	_, err = stream.Recv()
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestTranscribeSpeechStreamRejectsInvalidSampleRate(t *testing.T) {
	client := asrStreamClient(t, &mockASRProvider{})
	stream, err := client.TranscribeSpeechStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	start := validASRStart()
	start.SampleRateHz = 8000
	_ = stream.Send(&modelhubv2.TranscribeSpeechClientMessage{
		Item: &modelhubv2.TranscribeSpeechClientMessage_Start{Start: start},
	})
	_, err = stream.Recv()
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}
