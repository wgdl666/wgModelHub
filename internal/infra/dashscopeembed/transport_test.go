package dashscopeembed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

// Exercise ALPN and the provider's real client: checking Transport fields alone
// misses a cloned TLS config that still advertises h2 after HTTP/2 is disabled.
func TestEmbeddingUploadsUseHTTP1WithoutChangingOtherProviders(t *testing.T) {
	image := bytes.Repeat([]byte("fixture-image"), 16384)
	protocols := make(chan int, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocols <- r.ProtoMajor
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/api/v1/services/embeddings/multimodal-embedding/multimodal-embedding" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("unexpected embedding path or credential header")
		}
		var payload struct {
			Model string `json:"model"`
			Input struct {
				Contents []map[string]string `json:"contents"`
			} `json:"input"`
			Parameters struct {
				Dimension int `json:"dimension"`
			} `json:"parameters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != models.Qwen3VLEmbedding || payload.Parameters.Dimension != 1024 || len(payload.Input.Contents) != 1 || payload.Input.Contents[0]["image"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(image) {
			t.Error("protocol fix must preserve the original image, model and vector dimension")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output": map[string]any{"embeddings": []any{map[string]any{"embedding": make([]float64, 1024)}}}})
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	previous := http.DefaultTransport
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	transport.Protocols.SetHTTP2(true)
	transport.TLSClientConfig.NextProtos = []string{"h2", "http/1.1"}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previous
		transport.CloseIdleConnections()
	})

	p, err := New("embedding", server.URL+"/api/v1", "fixture-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Generate(context.Background(), models.Qwen3VLEmbedding, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: image}}}}}}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-protocols; got != 1 {
		t.Errorf("embedding upload negotiated HTTP/%d; want HTTP/1.1", got)
	}
	response, err := (&http.Client{Timeout: time.Second}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if got := <-protocols; got != 2 {
		t.Errorf("default transport negotiated HTTP/%d; other providers must retain HTTP/2", got)
	}
}
