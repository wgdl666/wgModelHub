package openai

import (
	"testing"

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
)

func TestBuildRequestBodyPreservesZeroTemperature(t *testing.T) {
	zero := 0.0
	provider := &Provider{sendEnableThinking: true}
	body := provider.buildRequestBody("qwen-flash", &modelhubv1.GenerateTextRequest{
		Temperature: &zero,
		Thinking:    modelhubv1.ThinkingMode_THINKING_MODE_DISABLED,
		Messages: []*modelhubv1.Message{
			{Role: modelhubv1.Role_ROLE_USER, Parts: []*modelhubv1.ContentPart{{Content: &modelhubv1.ContentPart_Text{Text: "hi"}}}},
		},
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
}
