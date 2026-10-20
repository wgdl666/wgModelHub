package bedrockrerank

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type captureAPI struct {
	input *bedrockagentruntime.RerankInput
}

func (c *captureAPI) Rerank(_ context.Context, input *bedrockagentruntime.RerankInput, _ ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RerankOutput, error) {
	c.input = input
	return &bedrockagentruntime.RerankOutput{}, nil
}

func TestInstructionKeyRendersBedrockQuery(t *testing.T) {
	api := &captureAPI{}
	p := &Provider{name: "test", region: "us-east-2", client: api, Instructions: func() map[string]config.RerankTemplate {
		return map[string]config.RerankTemplate{"look": {Instruct: "all conditions", QueryTemplate: "外观「{{query}}」"}}
	}}
	req := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: `{"query":"白色通勤","documents":["shirt"],"instruction_key":"look"}`}}}}}}}}}
	if _, err := p.Generate(context.Background(), models.CohereRerankV35, req); err != nil {
		t.Fatal(err)
	}
	if got := *api.input.Queries[0].TextQuery.Text; got != "外观「白色通勤」" {
		t.Fatalf("query=%q", got)
	}
	if got := *api.input.Sources[0].InlineDocumentSource.TextDocument.Text; got != "shirt" {
		t.Fatalf("document=%q", got)
	}
}
