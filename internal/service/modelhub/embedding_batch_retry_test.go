package modelhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/cohereembed"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 批量不新增重试策略，瞬态失败仍由既有 Service 机制重试整个逻辑请求。
func TestCohereBatchUsesExistingServiceRetryPolicy(t *testing.T) {
	restore := shortenTextRetry(t)
	defer restore()
	var calls atomic.Int64
	texts := []string{"a", "b", "c"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || !reflect.DeepEqual(payload.Texts, texts) {
			t.Error("invalid batch payload")
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	cfg := &config.CohereEmbeddingProviderConfig{BaseURL: server.URL, APIKey: "test"}
	embedder, err := cohereembed.New("cohere", cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(config.Config{Providers: map[string]config.ProviderConfig{"cohere": {Models: []string{models.CohereEmbedV4}, CohereEmbedding: cfg}}}, map[string]provider.Set{"cohere": {Text: embedder}}, nil)
	request := textRequest(models.CohereEmbedV4, "unused")
	request.Input = &modelhubv2.Input{Embedding: &modelhubv2.EmbeddingInput{Texts: texts}}
	recorder := &generateRecorder{ctx: context.Background()}
	err = service.Generate(request, recorder)
	if status.Code(err) != codes.Unavailable || len(recorder.events) != 0 {
		t.Fatalf("err=%v events=%v", err, recorder.events)
	}
	if calls.Load() != 4 {
		t.Fatalf("unexpected existing retry policy: %d calls", calls.Load())
	}
}
