package minimaxtts

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

type fakeMode int

const (
	modeOK fakeMode = iota
	modeFailFirstAudio
	modeFailMidAudio
	modeHangAfterStart
)

type fakeTTSServer struct {
	mode        fakeMode
	closed      atomic.Bool
	gotContinue atomic.Bool
	mu          sync.Mutex
	conns       int
}

func (f *fakeTTSServer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.conns++
		f.mu.Unlock()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		defer f.closed.Store(true)

		_ = conn.WriteJSON(map[string]string{"event": eventConnectedSuccess})
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var start map[string]any
		if err := json.Unmarshal(msg, &start); err != nil || start["event"] != eventTaskStart {
			_ = conn.WriteJSON(map[string]any{"event": eventTaskFailed, "base_resp": map[string]any{"status_msg": "bad start"}})
			return
		}
		_ = conn.WriteJSON(map[string]string{"event": eventTaskStarted})

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var evt map[string]any
			if err := json.Unmarshal(msg, &evt); err != nil {
				return
			}
			switch evt["event"] {
			case eventTaskContinue:
				f.gotContinue.Store(true)
			case eventTaskFinish:
				if f.mode == modeHangAfterStart {
					// 故意不回 task_finished；阻塞在读循环上，客户端 Close 后应退出。
					for {
						if _, _, err := conn.ReadMessage(); err != nil {
							return
						}
					}
				}
				switch f.mode {
				case modeFailFirstAudio:
					_ = conn.WriteJSON(map[string]any{
						"event":     eventTaskFailed,
						"base_resp": map[string]any{"status_msg": "first fail"},
					})
				case modeFailMidAudio:
					chunk := hex.EncodeToString([]byte("ID3partial"))
					_ = conn.WriteJSON(map[string]any{"data": map[string]any{"audio": chunk}})
					_ = conn.WriteJSON(map[string]any{
						"event":     eventTaskFailed,
						"base_resp": map[string]any{"status_msg": "mid fail"},
					})
				default:
					chunk := hex.EncodeToString([]byte("ID3complete-mp3-bytes"))
					_ = conn.WriteJSON(map[string]any{"data": map[string]any{"audio": chunk}})
					_ = conn.WriteJSON(map[string]string{"event": eventTaskFinished})
				}
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http")
}

func TestSynthesizeSpeechSuccessReturnsFullAudio(t *testing.T) {
	fake := &fakeTTSServer{mode: modeOK}
	srv := fake.serve(t)
	p, err := New(Config{Name: "tts", APIKey: "k", Endpoint: wsURL(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.SynthesizeSpeech(context.Background(), models.Speech28Turbo, &modelhubv2.SynthesizeSpeechRequest{
		Model: models.Speech28Turbo,
		Text:  "你好",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetAudio().GetMimeType() != mimeMP3 {
		t.Fatalf("mime=%q", resp.GetAudio().GetMimeType())
	}
	if got := string(resp.GetAudio().GetData()); got != "ID3complete-mp3-bytes" {
		t.Fatalf("audio=%q", got)
	}
}

func TestSynthesizeSpeechRejectsEmptyAndOverlongText(t *testing.T) {
	p, err := New(Config{Name: "tts", APIKey: "k", Endpoint: "ws://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.SynthesizeSpeech(context.Background(), models.Speech28Turbo, &modelhubv2.SynthesizeSpeechRequest{Text: "  "})
	if provider.Kind(err) != provider.ErrorInvalidArgument {
		t.Fatalf("empty text: %v", err)
	}
	long := strings.Repeat("啊", protocol.MaxSpeechTextChars)
	_, err = p.SynthesizeSpeech(context.Background(), models.Speech28Turbo, &modelhubv2.SynthesizeSpeechRequest{Text: long})
	if provider.Kind(err) != provider.ErrorInvalidArgument {
		t.Fatalf("overlong: %v", err)
	}
}

func TestSynthesizeSpeechUpstreamFailBeforeAudio(t *testing.T) {
	fake := &fakeTTSServer{mode: modeFailFirstAudio}
	srv := fake.serve(t)
	p, err := New(Config{Name: "tts", APIKey: "k", Endpoint: wsURL(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.SynthesizeSpeech(context.Background(), models.Speech28Turbo, &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
	if err == nil || resp != nil {
		t.Fatalf("expected failure, resp=%v err=%v", resp, err)
	}
	if provider.Kind(err) != provider.ErrorUnavailable {
		t.Fatalf("kind=%s err=%v", provider.Kind(err), err)
	}
}

func TestSynthesizeSpeechUpstreamFailMidAudioNotPartialSuccess(t *testing.T) {
	fake := &fakeTTSServer{mode: modeFailMidAudio}
	srv := fake.serve(t)
	p, err := New(Config{Name: "tts", APIKey: "k", Endpoint: wsURL(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.SynthesizeSpeech(context.Background(), models.Speech28Turbo, &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
	if err == nil || resp != nil {
		t.Fatalf("partial success leaked: resp=%v err=%v", resp, err)
	}
}

func TestSynthesizeSpeechCancelClosesUpstream(t *testing.T) {
	fake := &fakeTTSServer{mode: modeHangAfterStart}
	srv := fake.serve(t)
	p, err := New(Config{Name: "tts", APIKey: "k", Endpoint: wsURL(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := p.SynthesizeSpeech(ctx, models.Speech28Turbo, &modelhubv2.SynthesizeSpeechRequest{Text: "hi"})
		errCh <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.gotContinue.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !fake.gotContinue.Load() {
		cancel()
		t.Fatal("server did not receive task_continue")
	}
	// 给 Flush 一点时间进入等待，再取消。
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected cancel error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("synthesize did not return after cancel")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.closed.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("upstream websocket was not closed after cancel")
}
