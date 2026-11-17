package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/wgdl666/kangaroo/callmeta"
)

func TestGatewayWrapsChatWithoutChangingParametersOrStream(t *testing.T) {
	const response = "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}],\"usage\":{\"total_tokens\":7}}\n\ndata: [DONE]\n\n"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/gateway/requests/registered-chat" || r.Header.Get("Authorization") != "Bearer execution" {
			t.Error("wrong execution target")
		}
		if _, err := uuid.Parse(r.Header.Get("Idempotency-Key")); err != nil {
			t.Error("missing request ID")
		}
		if r.Header.Get(callmeta.DeviceID) != "device" || r.Header.Get(callmeta.UserID) != "user" {
			t.Error("source identity lost")
		}
		var envelope struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
		}
		if envelope.Input["model"] != nil || envelope.Input["reasoning_effort"] != "high" || envelope.Input["temperature"] != float64(0) || envelope.Input["stream"] != true || envelope.Input["model_traits"].(map[string]any)["task"] != "image_type" {
			t.Errorf("parameters changed: %+v", envelope.Input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, response)
	}))
	defer remote.Close()
	p, err := New("gateway", "execution", remote.URL+"/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	p.ModelGateway = true
	body := map[string]any{"model": "registered-chat", "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "reasoning_effort": "high", "temperature": 0, "stream": true, "model_traits": map[string]any{"task": "image_type"}}
	reader, err := p.doRequest(callmeta.WithIdentity(context.Background(), "device", "user"), body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil || string(raw) != response {
		t.Fatal("stream changed")
	}
	if body["model"] != "registered-chat" {
		t.Fatal("caller request mutated")
	}
}
