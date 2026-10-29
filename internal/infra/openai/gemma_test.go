package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

// 同一路径覆盖内网免鉴权与已有 Bearer，确保接入不改写模型名或影响其他实例。
func TestGemmaChatAuthentication(t *testing.T) {
	for _, key := range []string{"", "secret"} {
		t.Run("key="+key, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected endpoint %s %s", r.Method, r.URL.Path)
				}
				wantAuth := ""
				if key != "" {
					wantAuth = "Bearer " + key
				}
				if r.Header.Get("Authorization") != wantAuth {
					t.Errorf("unexpected authorization")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != models.Gemma426BA4BIT {
					t.Errorf("model=%v", body["model"])
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"test","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
			}))
			defer srv.Close()
			p, err := New("wgdl_gemma", key, srv.URL+"/v1", "")
			if err != nil {
				t.Fatal(err)
			}
			ev, err := p.Generate(context.Background(), models.Gemma426BA4BIT, systemUserRequest("", "hello", nil))
			if err != nil || ev == nil {
				t.Fatalf("Generate()=%v, %v", ev, err)
			}
		})
	}
}
