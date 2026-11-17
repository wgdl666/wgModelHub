package dashscopeembed

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) Do(req *http.Request) (*http.Response, error) { return r(req) }

func TestEmbedTextWithoutBatchFlagReturnsOneVector(t *testing.T) {
	provider, err := New("qwen", "https://dashscope.example/api/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	provider.client = roundTrip(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/multimodal-embedding") {
			t.Fatalf("path %s", r.URL.Path)
		}
		var payload struct {
			Input struct {
				Contents []map[string]string `json:"contents"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Input.Contents) != 1 || payload.Input.Contents[0]["text"] != "0\n1" {
			t.Fatalf("expected one combined text, got %v", payload.Input.Contents)
		}
		vector := make([]float64, vectorDim)
		vector[0] = 0.5
		return embeddingResponse(t, []embeddingRow{{Embedding: vector}}), nil
	})
	request := batchRequest(2)
	request.Input.ModelTraits = ""
	event, err := provider.Generate(context.Background(), models.Qwen3VLEmbedding, request)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Embedding) != vectorDim || result.Embedding[0] != 0.5 {
		t.Fatalf("expected one %d-dimensional vector, got length %d", vectorDim, len(result.Embedding))
	}
}
