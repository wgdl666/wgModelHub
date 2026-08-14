package ark

import (
	"testing"

	"github.com/volcengine/volcengine-go-sdk/service/arkruntime/model/responses"
	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
)

func TestConvertMessageToInputItemsEmitsAllToolCalls(t *testing.T) {
	items := convertMessageToInputItems(&modelhubv1.Message{
		Role: modelhubv1.Role_ROLE_ASSISTANT,
		ToolCalls: []*modelhubv1.ToolCall{
			{Id: "call-1", Name: "search", ArgumentsJson: []byte(`{"q":"a"}`)},
			{Id: "call-2", Name: "browse", ArgumentsJson: []byte(`{"page":1}`)},
		},
	})
	if len(items) != 2 {
		t.Fatalf("items len = %d", len(items))
	}
	first := items[0].GetFunctionToolCall()
	second := items[1].GetFunctionToolCall()
	if first == nil || second == nil {
		t.Fatalf("items = %#v", items)
	}
	if first.CallId != "call-1" || first.Name != "search" || first.Arguments != `{"q":"a"}` {
		t.Fatalf("first = %#v", first)
	}
	if second.CallId != "call-2" || second.Name != "browse" {
		t.Fatalf("second = %#v", second)
	}
	if first.Type != responses.ItemType_function_call || second.Type != responses.ItemType_function_call {
		t.Fatalf("unexpected item types")
	}
}

func TestBuildRequestPreservesZeroTemperatureAndPreviousResponseID(t *testing.T) {
	provider := &Provider{name: "ark"}
	zero := 0.0
	request := &modelhubv1.GenerateTextRequest{
		PreviousResponseId: "resp-prev",
		Temperature:        &zero,
		Messages: []*modelhubv1.Message{
			{
				Role: modelhubv1.Role_ROLE_USER,
				Parts: []*modelhubv1.ContentPart{
					{Content: &modelhubv1.ContentPart_Text{Text: "hello"}},
				},
			},
		},
	}
	arkReq, err := provider.buildRequest("model-x", request)
	if err != nil {
		t.Fatal(err)
	}
	if arkReq.Temperature == nil || *arkReq.Temperature != 0 {
		t.Fatalf("temperature = %#v", arkReq.Temperature)
	}
	if arkReq.PreviousResponseId == nil || *arkReq.PreviousResponseId != "resp-prev" {
		t.Fatalf("previous response id = %#v", arkReq.PreviousResponseId)
	}
}
