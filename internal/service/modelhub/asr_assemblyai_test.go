package modelhub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/asr/assemblyai"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAssemblyAISessionErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		errorFrame bool
		code       int
		wantStatus codes.Code
		wantReason string
	}{
		{"expiry_error_frame", true, 3008, codes.Unavailable, "ASR_SESSION_EXPIRED"},
		{"expiry_close_frame", false, 3008, codes.Unavailable, "ASR_SESSION_EXPIRED"},
		{"account_rejection", true, 1008, codes.Internal, "INVALID_RESPONSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				if err := conn.WriteJSON(map[string]any{"type": "Begin", "id": "test-session", "expires_at": time.Now().Add(3 * time.Hour).Unix()}); err != nil {
					t.Error(err)
					return
				}
				if _, _, err := conn.ReadMessage(); err != nil {
					t.Error(err)
					return
				}
				if tc.errorFrame {
					if err := conn.WriteJSON(map[string]any{"type": "Error", "error_code": tc.code, "error": fmt.Sprintf("supplier session error %d", tc.code)}); err != nil {
						t.Error(err)
						return
					}
				}
				if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(tc.code, "supplier close"), time.Now().Add(time.Second)); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(upstream.Close)
			adapter, err := assemblyai.New(assemblyai.Config{APIKey: "test", URL: "ws" + strings.TrimPrefix(upstream.URL, "http")})
			if err != nil {
				t.Fatal(err)
			}
			client := asrStreamClient(t, adapter)
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
			if status.Code(err) != tc.wantStatus {
				t.Fatalf("status=%v, want %v", err, tc.wantStatus)
			}
			if !strings.Contains(err.Error(), fmt.Sprint(tc.code)) {
				t.Fatalf("supplier code lost: %v", err)
			}
			for _, detail := range status.Convert(err).Details() {
				if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == "wg.modelhub" && info.Reason == tc.wantReason {
					return
				}
			}
			t.Fatalf("missing reason %s: %v", tc.wantReason, err)
		})
	}
}
