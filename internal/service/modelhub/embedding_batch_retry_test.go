package modelhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
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
	var mu sync.Mutex
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Texts) != 1 {
			t.Error("invalid chunk payload")
			w.WriteHeader(400)
			return
		}
		text := payload.Texts[0]
		mu.Lock()
		counts[text]++
		mu.Unlock()
		if text == "b" {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": map[string]any{"float": [][]float64{make([]float64, 1024)}}})
	}))
	defer server.Close()
	cfg := &config.CohereEmbeddingProviderConfig{BaseURL: server.URL, APIKey: "test", MaxBatchSize: 1}
	embedder, err := cohereembed.New("cohere", cfg)
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(config.Config{Providers: map[string]config.ProviderConfig{"cohere": {Models: []string{models.CohereEmbedV4}, CohereEmbedding: cfg}}}, map[string]provider.Set{"cohere": {Text: embedder}}, nil)
	request := textRequest(models.CohereEmbedV4, "unused")
	request.Input.ModelTraits = `{"embedding_batch":true,"input_type":"search_query"}`
	request.Input.Items = nil
	for _, text := range []string{"a", "b", "c"} {
		request.Input.Items = append(request.Input.Items, &modelhubv2.InputItem{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: text}}}}}})
	}
	recorder := &generateRecorder{ctx: context.Background()}
	err = service.Generate(request, recorder)
	if status.Code(err) != codes.Unavailable || len(recorder.events) != 0 {
		t.Fatalf("err=%v events=%v", err, recorder.events)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(counts, map[string]int{"a": 4, "b": 4}) {
		t.Fatalf("unexpected existing retry policy: %v", counts)
	}
}
