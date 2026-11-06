package dashscopeembed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func TestEmbedBatchSplitsAndOrdersByIndex(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxBatch  int
		wantSizes []int
	}{
		{name: "default", wantSizes: []int{20, 1}},
		{name: "configured", maxBatch: 10, wantSizes: []int{10, 10, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New("qwen", &config.DashScopeEmbeddingProviderConfig{
				BaseURL: "https://dashscope.example/api/v1", APIKey: "key", MaxBatchSize: tc.maxBatch,
			})
			if err != nil {
				t.Fatal(err)
			}
			var sizes []int
			p.client = roundTrip(func(req *http.Request) (*http.Response, error) {
				var payload struct {
					Model string `json:"model"`
					Input struct {
						Contents []struct{ Text string } `json:"contents"`
					} `json:"input"`
					Parameters struct {
						Fusion    *bool `json:"enable_fusion"`
						Dimension int   `json:"dimension"`
					} `json:"parameters"`
				}
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Model != models.Qwen3VLEmbedding || payload.Parameters.Fusion == nil ||
					*payload.Parameters.Fusion || payload.Parameters.Dimension != vectorDim {
					t.Fatalf("unexpected batch payload: %+v", payload)
				}
				contents := payload.Input.Contents
				sizes = append(sizes, len(contents))
				rows := make([]embeddingRow, len(contents))
				for i, item := range contents {
					value, err := strconv.Atoi(item.Text)
					if err != nil {
						t.Fatal(err)
					}
					vector := make([]float64, vectorDim)
					vector[0] = float64(value)
					rows[len(rows)-1-i] = embeddingRow{Index: &i, Embedding: vector}
				}
				return embeddingResponse(t, rows), nil
			})
			event, err := p.Generate(context.Background(), models.Qwen3VLEmbedding, batchRequest(21))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sizes, tc.wantSizes) {
				t.Fatalf("HTTP batch sizes = %v, want %v", sizes, tc.wantSizes)
			}
			var result struct {
				Embeddings [][]float64 `json:"embeddings"`
			}
			if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Embeddings) != 21 {
				t.Fatalf("got %d vectors", len(result.Embeddings))
			}
			for i, row := range result.Embeddings {
				if len(row) != vectorDim || row[0] != float64(i) {
					t.Fatalf("invalid result at input %d", i)
				}
			}
		})
	}
}

func TestEmbedBatchRejectsDuplicateIndexWithoutPublishingPartialResult(t *testing.T) {
	p, err := New("qwen", &config.DashScopeEmbeddingProviderConfig{
		BaseURL: "https://dashscope.example/api/v1", APIKey: "key", MaxBatchSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	p.client = roundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		first, second := 0, 1
		if calls == 2 {
			second = first
		}
		vector := make([]float64, vectorDim)
		return embeddingResponse(t, []embeddingRow{{Index: &first, Embedding: vector}, {Index: &second, Embedding: vector}}), nil
	})
	event, err := p.Generate(context.Background(), models.Qwen3VLEmbedding, batchRequest(5))
	if event != nil || provider.Kind(err) != provider.ErrorInvalidResponse || calls != 2 {
		t.Fatalf("event=%v err=%v calls=%d", event, err, calls)
	}
}

func batchRequest(count int) *modelhubv2.GenerateRequest {
	input := &modelhubv2.Input{ModelTraits: `{"embedding_batch":true,"input_type":"search_query"}`}
	for i := range count {
		input.Items = append(input.Items, &modelhubv2.InputItem{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: strconv.Itoa(i)}}},
			}},
		})
	}
	return &modelhubv2.GenerateRequest{Input: input}
}

func embeddingResponse(t *testing.T, rows []embeddingRow) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"output": map[string]any{"embeddings": rows}})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}
}
