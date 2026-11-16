package bedrockrerank

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/rerankbatch"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

type captureAPI struct {
	input    *bedrockagentruntime.RerankInput
	sizes    []int
	pageSize int
}

func (c *captureAPI) Rerank(_ context.Context, input *bedrockagentruntime.RerankInput, _ ...func(*bedrockagentruntime.Options)) (*bedrockagentruntime.RerankOutput, error) {
	c.input = input
	c.sizes = append(c.sizes, len(input.Sources))
	start, end := 0, len(input.Sources)
	if input.NextToken != nil {
		start, _ = strconv.Atoi(*input.NextToken)
	}
	if c.pageSize > 0 {
		end = min(start+c.pageSize, end)
	}
	results := make([]types.RerankResult, end-start)
	for index := range results {
		value := int32(start + index)
		results[index].Index = &value
	}
	output := &bedrockagentruntime.RerankOutput{Results: results}
	if end < len(input.Sources) {
		token := strconv.Itoa(end)
		output.NextToken = &token
	}
	return output, nil
}

func TestLogicalRequestCollectsPagesAndRejectsOversizeBeforeSDKCall(t *testing.T) {
	api := &captureAPI{pageSize: 100}
	p := &Provider{name: "test", region: "us-east-2", client: api}
	documents := make([]string, 1001)
	for index := range documents {
		documents[index] = "document"
	}
	for _, count := range []int{205, 1001} {
		raw, err := json.Marshal(rerankbatch.Request{Query: "query", Documents: documents[:count]})
		if err != nil {
			t.Fatal(err)
		}
		req := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: string(raw)}}}}}}}}}
		_, err = p.Generate(t.Context(), models.CohereRerankV35, req)
		if count == 205 && err != nil {
			t.Fatal(err)
		}
		if count == 1001 && (provider.Kind(err) != provider.ErrorInvalidArgument || !provider.IsNotAttempted(err)) {
			t.Fatalf("oversized request: %v", err)
		}
	}
	if !reflect.DeepEqual(api.sizes, []int{205, 205, 205}) {
		t.Fatalf("SDK calls=%v", api.sizes)
	}
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
