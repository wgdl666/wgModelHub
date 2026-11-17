package dashscopeembed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func TestEmbedBatchForwardsOnceAndOrdersByIndex(t *testing.T) {
	// 让模拟上游接受 21 条，以验证适配器不会按之前的 20 条上限切分。
	for _, count := range []int{1, 21} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			p, err := New("qwen", "https://dashscope.example/api/v1", "key")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			p.client = roundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
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
				if len(contents) != count {
					t.Fatalf("HTTP request has %d texts, want %d", len(contents), count)
				}
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
			event, err := p.Generate(context.Background(), models.Qwen3VLEmbedding, batchRequest(count))
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("got %d HTTP requests, want one", calls)
			}
			var result struct {
				Embeddings [][]float64 `json:"embeddings"`
			}
			if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Embeddings) != count {
				t.Fatalf("got %d vectors, want %d", len(result.Embeddings), count)
			}
			for i, row := range result.Embeddings {
				if len(row) != vectorDim || row[0] != float64(i) {
					t.Fatalf("invalid result at input %d", i)
				}
			}
		})
	}
}

func TestEmbedBatchRejectsDuplicateIndex(t *testing.T) {
	p, err := New("qwen", "https://dashscope.example/api/v1", "key")
	if err != nil {
		t.Fatal(err)
	}
	p.client = roundTrip(func(req *http.Request) (*http.Response, error) {
		index := 0
		vector := make([]float64, vectorDim)
		return embeddingResponse(t, []embeddingRow{{Index: &index, Embedding: vector}, {Index: &index, Embedding: vector}}), nil
	})
	event, err := p.Generate(context.Background(), models.Qwen3VLEmbedding, batchRequest(2))
	if event != nil || provider.Kind(err) != provider.ErrorInvalidResponse {
		t.Fatalf("event=%v err=%v", event, err)
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
