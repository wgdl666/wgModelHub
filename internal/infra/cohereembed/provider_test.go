package cohereembed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func logicalRequest(texts ...string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{
		Embedding: &modelhubv2.EmbeddingInput{Texts: texts},
	}}
}

func imageRequest() *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{
		Embedding: &modelhubv2.EmbeddingInput{Image: &modelhubv2.Media{
			MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("png")},
		}},
	}}
}

func testVectors(count int) [][]float64 {
	rows := make([][]float64, count)
	for i := range rows {
		rows[i] = make([]float64, vectorDim)
		rows[i][0] = float64(i + 1)
	}
	return rows
}

func TestGenerateUsesUnifiedEmbeddingContract(t *testing.T) {
	document := logicalRequest("衣物商品描述")
	document.Input.Embedding.SearchDocument = true
	for _, tc := range []struct {
		name, inputType string
		request         *modelhubv2.GenerateRequest
		texts, images   []string
	}{
		{name: "single", inputType: "search_query", request: logicalRequest(" coat "), texts: []string{"coat"}},
		{name: "batch", inputType: "search_query", request: logicalRequest("coat", "{look}"), texts: []string{"coat", "{look}"}},
		{name: "index document", inputType: "search_document", request: document, texts: []string{"衣物商品描述"}},
		{name: "index image", inputType: "search_document", request: imageRequest(), images: []string{"data:image/png;base64,cG5n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			want := testVectors(len(tc.texts) + len(tc.images))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var payload struct {
					Texts     []string `json:"texts"`
					Images    []string `json:"images"`
					InputType string   `json:"input_type"`
					Dimension int      `json:"output_dimension"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(payload.Texts, tc.texts) || !reflect.DeepEqual(payload.Images, tc.images) ||
					payload.InputType != tc.inputType || payload.Dimension != 1024 {
					t.Errorf("payload=%+v", payload)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": map[string]any{"float": want}})
			}))
			defer server.Close()
			p, err := New("cohere", &config.CohereEmbeddingProviderConfig{BaseURL: server.URL, APIKey: "test-key"})
			if err != nil {
				t.Fatal(err)
			}
			event, err := p.Generate(context.Background(), models.CohereEmbedV4, tc.request)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Embeddings [][]float64 `json:"embeddings"`
			}
			if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || !reflect.DeepEqual(result.Embeddings, want) {
				t.Fatalf("calls=%d rows=%d, want one call and %d rows", calls.Load(), len(result.Embeddings), len(want))
			}
		})
	}
}

func TestGenerateRejectsLegacyAndMixedInputsBeforeHTTP(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{})
	p.client = embeddingDoer(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached the provider")
		return nil, nil
	})
	mixed := imageRequest()
	mixed.Input.Embedding.Texts = []string{"coat"}
	legacy := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{
		ModelTraits: `{"embedding_batch":true,"input_type":"search_query"}`,
		Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "coat"}}},
		}}}},
	}}
	for _, request := range []*modelhubv2.GenerateRequest{legacy, mixed, logicalRequest(), logicalRequest(" ")} {
		_, err := p.Generate(context.Background(), models.CohereEmbedV4, request)
		if provider.Kind(err) != provider.ErrorInvalidArgument || !provider.IsNotAttempted(err) {
			t.Fatalf("invalid request error=%v", err)
		}
	}
}

func TestPostRejectsMalformedCountAndDimensions(t *testing.T) {
	nullValues := make([]any, vectorDim)
	for i := range nullValues {
		nullValues[i] = 0
	}
	nullValues[1] = nil
	wrongDim := testVectors(2)
	wrongDim[1] = []float64{1}
	for _, response := range []any{
		map[string]any{"embeddings": testVectors(1)},
		map[string]any{"embeddings": wrongDim},
		map[string]any{"embeddings": []any{nullValues, nullValues}},
		map[string]any{"embeddings": map[string]any{"int8": [][]int{{1}}}},
		map[string]any{"embeddings": [][]float64{}},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(response) }))
		p, _ := New("cohere", &config.CohereEmbeddingProviderConfig{BaseURL: server.URL, APIKey: "test"})
		if _, err := p.post(context.Background(), map[string]any{}, 2); err == nil {
			t.Errorf("accepted invalid response %v", response)
		}
		server.Close()
	}
	for _, raw := range []string{`{"embeddings":{"float":[[1e1000]]}}`, `broken`, strings.Repeat(" ", maxResponseBytes+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(raw)) }))
		p, _ := New("cohere", &config.CohereEmbeddingProviderConfig{BaseURL: server.URL, APIKey: "test"})
		if _, err := p.post(context.Background(), map[string]any{}, 1); err == nil {
			t.Fatal("accepted malformed/oversized response")
		}
		server.Close()
	}
}

func TestPostAcceptsLargeBatchAndCancellation(t *testing.T) {
	rows := testVectors(96)
	for _, row := range rows {
		for i := range row {
			row[i] = 0.1234567890123456
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": rows})
	}))
	defer server.Close()
	p, _ := New("cohere", &config.CohereEmbeddingProviderConfig{BaseURL: server.URL, APIKey: "test"})
	if got, err := p.post(context.Background(), map[string]any{}, 96); err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("large batch failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.post(ctx, map[string]any{}, 96); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}
