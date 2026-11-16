package modelhub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/asr/assemblyai"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func assemblyAIStreamClient(t *testing.T, serve func(*websocket.Conn) error) modelhubv2.ModelHubServiceClient {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if err := serve(conn); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(upstream.Close)
	adapter, err := assemblyai.New(assemblyai.Config{APIKey: "test", URL: "ws" + strings.TrimPrefix(upstream.URL, "http")})
	if err != nil {
		t.Fatal(err)
	}
	return asrStreamClient(t, adapter)
}

func TestAssemblyAISessionWaitsForBegin(t *testing.T) {
	connected, release := make(chan struct{}), make(chan struct{})
	releaseBegin := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseBegin)
	expires := time.Now().Add(3 * time.Hour).Unix()
	client := assemblyAIStreamClient(t, func(conn *websocket.Conn) error {
		close(connected)
		<-release
		if err := conn.WriteJSON(map[string]any{"type": "Begin", "id": "supplier-session", "expires_at": expires}); err != nil {
			return err
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			return err
		}
		return conn.WriteJSON(map[string]string{"type": "Termination"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.TranscribeSpeechStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Start{Start: validASRStart()}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("supplier did not connect")
	}
	type received struct {
		message *modelhubv2.TranscribeSpeechServerMessage
		err     error
	}
	reply := make(chan received, 1)
	go func() { message, err := stream.Recv(); reply <- received{message, err} }()
	select {
	case got := <-reply:
		releaseBegin()
		t.Fatalf("session acknowledged before Begin: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	releaseBegin()
	got := <-reply
	if got.err != nil || got.message.GetSession().GetSessionId() != "supplier-session" || got.message.GetSession().GetExpiresAt().GetSeconds() != expires {
		t.Fatalf("session=%v err=%v", got.message, got.err)
	}
	if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Stop{Stop: &modelhubv2.TranscribeSpeechStop{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != io.EOF {
		t.Fatalf("stop: %v", err)
	}
}

func TestAssemblyAIStopDrainsWithAndWithoutFinal(t *testing.T) {
	for _, final := range []bool{true, false} {
		t.Run(fmt.Sprintf("final=%v", final), func(t *testing.T) {
			client := assemblyAIStreamClient(t, func(conn *websocket.Conn) error {
				if err := conn.WriteJSON(map[string]any{"type": "Begin", "id": "drain-session", "expires_at": time.Now().Add(3 * time.Hour).Unix()}); err != nil {
					return err
				}
				var stop struct {
					Type string `json:"type"`
				}
				if err := conn.ReadJSON(&stop); err != nil {
					return err
				}
				if stop.Type != "Terminate" {
					return fmt.Errorf("expected Terminate, got %q", stop.Type)
				}
				if final {
					if err := conn.WriteJSON(map[string]any{"type": "Turn", "transcript": "最后一句", "end_of_turn": true}); err != nil {
						return err
					}
				}
				return conn.WriteJSON(map[string]string{"type": "Termination"})
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, err := client.TranscribeSpeechStream(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Start{Start: validASRStart()}}); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Stop{Stop: &modelhubv2.TranscribeSpeechStop{}}}); err != nil {
				t.Fatal(err)
			}
			if err := stream.CloseSend(); err != nil {
				t.Fatal(err)
			}
			if final {
				msg, err := stream.Recv()
				if err != nil || msg.GetTranscript().GetText() != "最后一句" || !msg.GetTranscript().GetIsFinal() {
					t.Fatalf("last transcript=%v err=%v", msg, err)
				}
			}
			if _, err := stream.Recv(); err != io.EOF {
				t.Fatalf("drain: %v", err)
			}
		})
	}
}

func TestAssemblyAIExpiryPreservesRecoverableReason(t *testing.T) {
	for _, errorFrame := range []bool{true, false} {
		t.Run(fmt.Sprintf("error_frame=%v", errorFrame), func(t *testing.T) {
			client := assemblyAIStreamClient(t, func(conn *websocket.Conn) error {
				if err := conn.WriteJSON(map[string]any{"type": "Begin", "id": "expired-session", "expires_at": time.Now().Add(time.Minute).Unix()}); err != nil {
					return err
				}
				if _, _, err := conn.ReadMessage(); err != nil {
					return err
				}
				if errorFrame {
					if err := conn.WriteJSON(map[string]any{"type": "Error", "error_code": 3008, "error": "Session Expired: Maximum session duration exceeded"}); err != nil {
						return err
					}
				}
				return conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(3008, "Session Expired"), time.Now().Add(time.Second))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			stream, err := client.TranscribeSpeechStream(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Start{Start: validASRStart()}}); err != nil {
				t.Fatal(err)
			}
			if _, err := stream.Recv(); err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Audio{Audio: &modelhubv2.TranscribeSpeechAudio{Data: make([]byte, 6400)}}}); err != nil {
				t.Fatal(err)
			}
			_, err = stream.Recv()
			if status.Code(err) != codes.Unavailable {
				t.Fatalf("expiry status=%v", err)
			}
			for _, detail := range status.Convert(err).Details() {
				if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == "wg.modelhub" && info.Reason == "ASR_SESSION_EXPIRED" {
					return
				}
			}
			t.Fatalf("expiry reason missing: %v", err)
		})
	}
}

func TestAssemblyAICancelWhileWaitingForBegin(t *testing.T) {
	connected, closed := make(chan struct{}), make(chan struct{})
	client := assemblyAIStreamClient(t, func(conn *websocket.Conn) error {
		close(connected)
		defer close(closed)
		_, _, _ = conn.ReadMessage()
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.TranscribeSpeechStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Start{Start: validASRStart()}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("supplier did not connect")
	}
	cancel()
	if _, err := stream.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("canceled Begin wait kept supplier connection open")
	}
}

func TestAssemblyAIStopRejectsCloseWithoutTermination(t *testing.T) {
	client := assemblyAIStreamClient(t, func(conn *websocket.Conn) error {
		if err := conn.WriteJSON(map[string]any{"type": "Begin", "id": "missing-termination", "expires_at": time.Now().Add(3 * time.Hour).Unix()}); err != nil {
			return err
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			return err
		}
		return conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := client.TranscribeSpeechStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Start{Start: validASRStart()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&modelhubv2.TranscribeSpeechClientMessage{Item: &modelhubv2.TranscribeSpeechClientMessage_Stop{Stop: &modelhubv2.TranscribeSpeechStop{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "before Termination") {
		t.Fatalf("incomplete drain: %v", err)
	}
}
