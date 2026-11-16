package dashscopererank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/rerankbatch"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func requestWithDocuments(t *testing.T, count int) *modelhubv2.GenerateRequest {
	t.Helper()
	documents := make([]string, count)
	for index := range documents {
		documents[index] = "document"
	}
	raw, err := json.Marshal(rerankbatch.Request{Query: "query", Documents: documents})
	if err != nil {
		t.Fatal(err)
	}
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: string(raw)}}}}}}}}}
}

func TestProviderBatchBoundariesAndLogicalLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		model string
		count int
		sizes []int
	}{
		{name: "current text input", model: models.Qwen37TextRerank, count: 200, sizes: []int{200}},
		{name: "text native limit", model: models.Qwen37TextRerank, count: 500, sizes: []int{500}},
		{name: "text split", model: models.Qwen37TextRerank, count: 501, sizes: []int{500, 1}},
		{name: "text logical limit", model: models.Qwen37TextRerank, count: 1000, sizes: []int{500, 500}},
		{name: "VL split", model: models.Qwen3VLRerank, count: 205, sizes: []int{100, 100, 5}},
		{name: "reject text oversize", model: models.Qwen37TextRerank, count: 1001},
		{name: "reject VL oversize", model: models.Qwen3VLRerank, count: 1001},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sizes []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Documents []string `json:"documents"`
					TopN      int      `json:"top_n"`
					Input     struct {
						Documents []map[string]string `json:"documents"`
					} `json:"input"`
					Parameters struct {
						TopN int `json:"top_n"`
					} `json:"parameters"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				count, topN := len(payload.Documents), payload.TopN
				if test.model == models.Qwen3VLRerank {
					count, topN = len(payload.Input.Documents), payload.Parameters.TopN
					if r.URL.Path != nativePath {
						t.Errorf("native path=%s", r.URL.Path)
					}
				} else if r.URL.Path != "/compatible-api/v1/reranks" {
					t.Errorf("text path=%s", r.URL.Path)
				}
				if topN != count {
					t.Errorf("top_n=%d documents=%d", topN, count)
				}
				sizes = append(sizes, count)
				results := make([]scored, count)
				for index := range results {
					results[index] = scored{Index: index, RelevanceScore: float64(index) / float64(count)}
				}
				response := map[string]any{"results": results}
				if test.model == models.Qwen3VLRerank {
					response = map[string]any{"output": response}
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			p, err := New("test", server.URL+"/compatible-api/v1", "test-key")
			if err != nil {
				t.Fatal(err)
			}
			event, err := p.Generate(context.Background(), test.model, requestWithDocuments(t, test.count))
			if test.count > rerankbatch.MaxDocuments {
				if provider.Kind(err) != provider.ErrorInvalidArgument || !provider.IsNotAttempted(err) || len(sizes) != 0 {
					t.Fatalf("sizes=%v err=%v", sizes, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(sizes, test.sizes) {
				t.Fatalf("sizes=%v want=%v err=%v", sizes, test.sizes, err)
			}
			var decoded struct {
				Results []scored `json:"results"`
			}
			if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &decoded); err != nil || len(decoded.Results) != test.count {
				t.Fatalf("results=%d err=%v", len(decoded.Results), err)
			}
			position, start := 0, 0
			for _, count := range test.sizes {
				for index := count - 1; index >= 0; index-- {
					if got := decoded.Results[position].Index; got != start+index {
						t.Fatalf("position=%d index=%d want=%d", position, got, start+index)
					}
					position++
				}
				start += count
			}
		})
	}
}

func TestHTTPParameterErrorsAreNotTransient(t *testing.T) {
	for _, code := range []int{400, 422} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		p, err := New("test", server.URL, "test-key")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Generate(context.Background(), models.Qwen37TextRerank, requestWithDocuments(t, 1))
		server.Close()
		if provider.Kind(err) != provider.ErrorInvalidArgument || provider.IsNotAttempted(err) {
			t.Fatalf("status=%d err=%v", code, err)
		}
	}
}
