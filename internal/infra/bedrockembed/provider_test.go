package bedrockembed

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type invokeFunc func(*bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error)

func (f invokeFunc) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	return f(input)
}

func TestBatchUsesOneInvokeModel(t *testing.T) {
	texts := []string{"coat", "pants"}
	vectors := [][]float64{make([]float64, vectorDim), make([]float64, vectorDim)}
	vectors[0][0], vectors[1][0] = 0.5, 0.7
	calls := 0
	p := &Provider{client: invokeFunc(func(input *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		calls++
		var payload struct {
			Texts     []string `json:"texts"`
			InputType string   `json:"input_type"`
		}
		if err := json.Unmarshal(input.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(payload.Texts, texts) || payload.InputType != "search_query" ||
			aws.ToString(input.ModelId) != models.CohereEmbedV4Bedrock {
			t.Fatalf("unexpected Bedrock request %s", input.Body)
		}
		body, err := json.Marshal(map[string]any{"embeddings": map[string]any{"float": vectors}})
		return &bedrockruntime.InvokeModelOutput{Body: body}, err
	})}
	request := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{
		Embedding: &modelhubv2.EmbeddingInput{Texts: texts},
	}}
	event, err := p.Generate(context.Background(), models.CohereEmbedV4Bedrock, request)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !reflect.DeepEqual(result.Embeddings, vectors) {
		t.Fatalf("calls=%d rows=%d, want one call and ordered vectors", calls, len(result.Embeddings))
	}
}
