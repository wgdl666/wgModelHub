package ark

import (
	"testing"

	"github.com/volcengine/volcengine-go-sdk/service/arkruntime/model/responses"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

func TestConvertMessageToInputItemsEmitsAllToolCalls(t *testing.T) {
	items := convertMessageToInputItems(&modelhubv2.Message{
		Role: modelhubv2.Role_ROLE_ASSISTANT,
		ToolCalls: []*modelhubv2.ToolCall{
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
	p := &Provider{name: "ark"}
	zero := 0.0
	request := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			PreviousResponseId: "resp-prev",
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_SYSTEM,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "task"}}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "hello"}}},
				}}},
			},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{Temperature: &zero}}},
	}
	arkReq, err := p.buildRequest("model-x", request)
	if err != nil {
		t.Fatal(err)
	}
	if arkReq.Temperature == nil || *arkReq.Temperature != 0 {
		t.Fatalf("temperature = %#v", arkReq.Temperature)
	}
	if arkReq.PreviousResponseId == nil || *arkReq.PreviousResponseId != "resp-prev" {
		t.Fatalf("previous response id = %#v", arkReq.PreviousResponseId)
	}
	if arkReq.Instructions != nil {
		t.Fatalf("instructions should be unset, got %#v", arkReq.Instructions)
	}
	list := arkReq.Input.GetListValue()
	if list == nil || len(list.ListValue) != 2 {
		t.Fatalf("input items = %#v", arkReq.Input)
	}
}

func TestConvertMessageToInputItemsKeepsTextWithToolCalls(t *testing.T) {
	items := convertMessageToInputItems(&modelhubv2.Message{
		Role:  modelhubv2.Role_ROLE_ASSISTANT,
		Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "calling"}}},
		ToolCalls: []*modelhubv2.ToolCall{
			{Id: "call-1", Name: "search", ArgumentsJson: []byte(`{"q":"a"}`)},
			{Id: "call-2", Name: "browse", ArgumentsJson: []byte(`{"page":1}`)},
		},
	})
	if len(items) != 3 {
		t.Fatalf("items len = %d", len(items))
	}
	msg := items[0].GetEasyMessage()
	if msg == nil || msg.Content.GetStringValue() != "calling" {
		t.Fatalf("text message lost: %#v", items[0])
	}
	if items[1].GetFunctionToolCall() == nil || items[2].GetFunctionToolCall() == nil {
		t.Fatalf("tool calls lost: %#v", items)
	}
}

func TestBuildInputKeepsMessageAndToolOutputOrder(t *testing.T) {
	p := &Provider{name: "ark"}
	request := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "ask"}}},
				}}},
				{Item: &modelhubv2.InputItem_ToolOutput{ToolOutput: &modelhubv2.ToolOutput{
					ToolCallId: "c1",
					Output:     `{"ok":true}`,
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "followup"}}},
				}}},
			},
		},
	}
	input := p.buildInput(request)
	list := input.GetListValue()
	if list == nil || len(list.ListValue) != 3 {
		t.Fatalf("input = %#v", input)
	}
	if list.ListValue[0].GetEasyMessage() == nil {
		t.Fatalf("first should remain message, got %#v", list.ListValue[0])
	}
	if list.ListValue[1].GetFunctionToolCallOutput() == nil {
		t.Fatalf("second should be tool output, got %#v", list.ListValue[1])
	}
	if list.ListValue[2].GetEasyMessage() == nil {
		t.Fatalf("third should remain message, got %#v", list.ListValue[2])
	}
}

func TestBuildRequestCachingExplicitSemantics(t *testing.T) {
	p := &Provider{name: "ark"}
	base := func(caching *modelhubv2.CachingConfig) *modelhubv2.GenerateRequest {
		return &modelhubv2.GenerateRequest{
			Input: &modelhubv2.Input{
				Caching: caching,
				Items: []*modelhubv2.InputItem{{
					Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
						Role:  modelhubv2.Role_ROLE_USER,
						Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "hi"}}},
					}},
				}},
			},
			Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
		}
	}

	arkReq, err := p.buildRequest("model-x", base(nil))
	if err != nil {
		t.Fatal(err)
	}
	if arkReq.Caching != nil || arkReq.ExpireAt != nil {
		t.Fatalf("provider must not invent caching when omitted: %#v", arkReq)
	}

	arkReq, err = p.buildRequest("model-x", base(&modelhubv2.CachingConfig{Enabled: true, ExpireAtUnix: 1700000000}))
	if err != nil {
		t.Fatal(err)
	}
	if arkReq.Caching == nil || arkReq.Caching.Type == nil || *arkReq.Caching.Type != responses.CacheType_enabled {
		t.Fatalf("explicit enabled caching = %#v", arkReq.Caching)
	}
	if arkReq.ExpireAt == nil || *arkReq.ExpireAt != 1700000000 {
		t.Fatalf("expire_at = %#v", arkReq.ExpireAt)
	}

	arkReq, err = p.buildRequest("model-x", base(&modelhubv2.CachingConfig{Enabled: false, ExpireAtUnix: 1700000000}))
	if err != nil {
		t.Fatal(err)
	}
	if arkReq.Caching != nil || arkReq.ExpireAt != nil {
		t.Fatalf("explicit disabled must not enable caching: caching=%#v expire=%#v", arkReq.Caching, arkReq.ExpireAt)
	}
}
