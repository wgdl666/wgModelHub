package bedrockembed

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type fakeInvoke func(*bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error)

func (f fakeInvoke) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	return f(input)
}

func TestBatchKeepsBedrockSingleTextProtocol(t *testing.T) {
	var seen []string
	p := &Provider{client: fakeInvoke(func(input *bedrockruntime.InvokeModelInput) (*bedrockruntime.InvokeModelOutput, error) {
		var body struct {
			Texts []string `json:"texts"`
		}
		if err := json.Unmarshal(input.Body, &body); err != nil || len(body.Texts) != 1 {
			t.Fatalf("expected a single text, got %q: %v", input.Body, err)
		}
		seen = append(seen, body.Texts[0])
		vector := make([]float64, vectorDim)
		vector[0] = float64(len(seen))
		encoded, err := json.Marshal(map[string]any{"embeddings": [][]float64{vector}})
		return &bedrockruntime.InvokeModelOutput{Body: encoded}, err
	})}
	texts := []string{"coat", "pants"}
	input := &modelhubv2.Input{ModelTraits: `{"embedding_batch":true,"input_type":"search_query"}`}
	for _, text := range texts {
		input.Items = append(input.Items, &modelhubv2.InputItem{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: text}}},
			}},
		})
	}
	event, err := p.Generate(context.Background(), models.CohereEmbedV4Bedrock, &modelhubv2.GenerateRequest{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, texts) || len(result.Embeddings) != 2 ||
		result.Embeddings[0][0] != 1 || result.Embeddings[1][0] != 2 {
		t.Fatal("Bedrock did not preserve one upstream request per text and ordered batch output")
	}
}
