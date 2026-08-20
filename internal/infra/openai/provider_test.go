package openai

import (
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestBuildRequestBodyKeepsMessageAndToolOutputOrder(t *testing.T) {
	p := &Provider{}
	body := p.buildRequestBody("gpt-4.1-mini", &modelhubv2.GenerateRequest{
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
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, false)
	messages, ok := body["messages"].([]map[string]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "ask" {
		t.Fatalf("first message lost: %#v", messages[0])
	}
	if messages[1]["role"] != "tool" || messages[1]["tool_call_id"] != "c1" {
		t.Fatalf("tool output lost: %#v", messages[1])
	}
	if messages[2]["role"] != "user" || messages[2]["content"] != "followup" {
		t.Fatalf("followup lost: %#v", messages[2])
	}
}

func TestConvertMessageKeepsTextWithToolCalls(t *testing.T) {
	msg := convertMessage(&modelhubv2.Message{
		Role:  modelhubv2.Role_ROLE_ASSISTANT,
		Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "calling"}}},
		ToolCalls: []*modelhubv2.ToolCall{{
			Id:            "call-1",
			Name:          "lookup",
			ArgumentsJson: []byte(`{"q":"x"}`),
		}},
	})
	if msg["content"] != "calling" {
		t.Fatalf("text lost: %#v", msg)
	}
	tcs, ok := msg["tool_calls"].([]map[string]any)
	if !ok || len(tcs) != 1 || tcs[0]["id"] != "call-1" {
		t.Fatalf("tool_calls = %#v", msg["tool_calls"])
	}
}

func TestBuildRequestBodyPreservesZeroTemperature(t *testing.T) {
	zero := 0.0
	p := &Provider{sendEnableThinking: true}
	body := p.buildRequestBody(models.QwenFlash, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "hi"}}},
				}},
			}},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{
			Temperature: &zero,
			Thinking:    modelhubv2.ThinkingMode_THINKING_MODE_DISABLED,
		}}},
	}, false)
	value, ok := body["temperature"].(float64)
	if !ok || value != 0 {
		t.Fatalf("temperature = %#v", body["temperature"])
	}
	thinking, ok := body["enable_thinking"].(bool)
	if !ok || thinking {
		t.Fatalf("enable_thinking = %#v", body["enable_thinking"])
	}
}

func TestConvertResponsePreservesIDAndUsageDetails(t *testing.T) {
	response := convertResponse(&chatCompletionResponse{
		ID: "chatcmpl-1",
		Choices: []chatChoice{{
			Message:      chatMessage{Content: "ok"},
			FinishReason: "stop",
		}},
		Usage: &apiUsage{
			PromptTokens:     10,
			CompletionTokens: 4,
			TotalTokens:      14,
			PromptTokensDetails: &apiPromptTokensDetails{
				CachedTokens: 6,
			},
			CompletionTokensDetails: &apiCompletionTokensDetails{
				ReasoningTokens: 2,
			},
		},
	})
	if response.GetResponseId() != "chatcmpl-1" {
		t.Fatalf("response id = %q", response.GetResponseId())
	}
	if response.GetUsage().GetCachedTokens() != 6 || response.GetUsage().GetReasoningTokens() != 2 {
		t.Fatalf("usage = %#v", response.GetUsage())
	}
	if len(response.GetItems()) != 1 || response.GetItems()[0].GetText() != "ok" {
		t.Fatalf("items = %#v", response.GetItems())
	}
}
