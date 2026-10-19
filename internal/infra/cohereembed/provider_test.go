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

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func textRequest(traits string, texts ...string) *modelhubv2.GenerateRequest {
	input := &modelhubv2.Input{ModelTraits: traits}
	for _, text := range texts {
		input.Items = append(input.Items, &modelhubv2.InputItem{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: text}}},
		}}})
	}
	return &modelhubv2.GenerateRequest{Input: input}
}

func testVectors(count int) [][]float64 {
	rows := make([][]float64, count)
	for i := range rows {
		rows[i] = make([]float64, vectorDim)
		rows[i][0] = float64(i + 1)
	}
	return rows
}

func TestGeneratePreservesSingleAndExplicitBatchContracts(t *testing.T) {
	for _, tc := range []struct {
		name, traits, inputType string
		texts, want             []string
		batch                   bool
	}{
		{name: "single", texts: []string{" coat "}, want: []string{"coat"}, inputType: "search_query"},
		{name: "document", texts: []string{`{"color":"red"}`}, want: []string{`{"color":"red"}`}, inputType: "search_document"},
		{name: "ordinary multiple messages remain one vector", texts: []string{"red", "coat"}, want: []string{"red\ncoat"}, inputType: "search_query"},
		{name: "batch one retains plural output", traits: `{"embedding_batch":true,"input_type":"search_query"}`, texts: []string{"coat"}, want: []string{"coat"}, inputType: "search_query", batch: true},
		{name: "batch ordered even JSON text is query", traits: `{"embedding_batch":true,"input_type":"search_query"}`, texts: []string{"coat", `{look}`}, want: []string{"coat", `{look}`}, inputType: "search_query", batch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v2/embed" || r.Header.Get("authorization") != "Bearer test-key" {
					t.Error("wrong provider request")
				}
				var payload struct {
					Texts     []string `json:"texts"`
					InputType string   `json:"input_type"`
					Dimension int      `json:"output_dimension"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(payload.Texts, tc.want) || payload.InputType != tc.inputType || payload.Dimension != 1024 {
					t.Errorf("payload=%+v", payload)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": map[string]any{"float": testVectors(len(tc.want))}})
			}))
			defer server.Close()
			p, err := New("cohere", server.URL, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			event, err := p.Generate(context.Background(), models.CohereEmbedV4, textRequest(tc.traits, tc.texts...))
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil {
				t.Fatal(err)
			}
			if tc.batch {
				var rows [][]float64
				if err := json.Unmarshal(result["embeddings"], &rows); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(rows, testVectors(len(tc.want))) || result["embedding"] != nil {
					t.Fatal("batch lost order or returned single shape")
				}
			} else if result["embedding"] == nil || result["embeddings"] != nil {
				t.Fatal("single shape changed")
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
		})
	}
}

func TestPayloadImageAndBatchValidation(t *testing.T) {
	image := textRequest("")
	image.Input.Items = []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("png")}}}}}}}}}
	payload, batch, err := coherePayload(models.CohereEmbedV4, image)
	if err != nil || batch || payload["input_type"] != "search_document" || payload["images"].([]string)[0] != "data:image/png;base64,cG5n" {
		t.Fatalf("image payload=%v batch=%v err=%v", payload, batch, err)
	}
	for _, traits := range []string{`{`, `[]`, `null`, `{"embedding_batch":"true"}`, `{"embedding_batch":true}`, `{"embedding_batch":true,"input_type":"search_document"}`} {
		if _, _, err := coherePayload(models.CohereEmbedV4, textRequest(traits, "coat")); err == nil {
			t.Errorf("accepted invalid traits %q", traits)
		}
	}
	traits := `{"embedding_batch":true,"input_type":"search_query"}`
	for _, texts := range [][]string{nil, {" "}, make([]string, 97)} {
		if _, _, err := coherePayload(models.CohereEmbedV4, textRequest(traits, texts...)); err == nil {
			t.Errorf("accepted invalid batch length %d", len(texts))
		}
	}
	texts := make([]string, 96)
	for i := range texts {
		texts[i] = "coat"
	}
	if _, _, err := coherePayload(models.CohereEmbedV4, textRequest(traits, texts...)); err != nil {
		t.Fatal(err)
	}
	image.Input.ModelTraits = traits
	if _, _, err := coherePayload(models.CohereEmbedV4, image); err == nil {
		t.Fatal("accepted image in query batch")
	}
	multiple := textRequest(traits, "coat")
	multiple.Input.Items[0].GetMessage().Parts = append(multiple.Input.Items[0].GetMessage().Parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Text{Text: "red"}})
	if _, _, err := coherePayload(models.CohereEmbedV4, multiple); err == nil {
		t.Fatal("accepted ambiguous multi-part batch item")
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
		p, _ := New("cohere", server.URL, "test")
		if _, err := p.post(context.Background(), map[string]any{}, 2); err == nil {
			t.Errorf("accepted invalid response %v", response)
		}
		server.Close()
	}
	for _, raw := range []string{`{"embeddings":{"float":[[1e1000]]}}`, `broken`, strings.Repeat(" ", maxResponseBytes+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(raw)) }))
		p, _ := New("cohere", server.URL, "test")
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
	p, _ := New("cohere", server.URL, "test")
	if got, err := p.post(context.Background(), map[string]any{}, 96); err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("large batch failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.post(ctx, map[string]any{}, 96); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}
