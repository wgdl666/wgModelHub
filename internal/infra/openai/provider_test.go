package openai

import (
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

const dashScopeBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

func dashScopeProvider() *Provider {
	return &Provider{baseURL: dashScopeBaseURL}
}

func systemUserRequest(systemText, userText string, caching *modelhubv2.CachingConfig) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Caching: caching,
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_SYSTEM,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: systemText}}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: userText}}},
				}}},
			},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{
			Thinking: modelhubv2.ThinkingMode_THINKING_MODE_DISABLED,
		}}},
	}
}

func systemCacheControl(t *testing.T, body map[string]any) (map[string]any, bool) {
	t.Helper()
	messages, ok := body["messages"].([]map[string]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	if messages[0]["role"] != "system" {
		t.Fatalf("first message = %#v", messages[0])
	}
	parts, ok := messages[0]["content"].([]map[string]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("system content = %#v", messages[0]["content"])
	}
	last := parts[len(parts)-1]
	cc, ok := last["cache_control"].(map[string]any)
	return cc, ok
}

func TestBuildRequestBodyDashScopeExplicitCacheControl(t *testing.T) {
	systemText := strings.Repeat("缓存前缀。", 400) // 稳定 system 文本，语义上等价于 >1024 token 前缀。
	p := dashScopeProvider()
	caching := &modelhubv2.CachingConfig{Enabled: true}

	for _, model := range []string{models.QwenFlash, models.Qwen37Flash, models.Qwen35Flash, models.Qwen3VLPlus} {
		t.Run(model+"_attaches_ephemeral", func(t *testing.T) {
			body := p.buildRequestBody(model, systemUserRequest(systemText, "hi", caching), false)
			cc, ok := systemCacheControl(t, body)
			if !ok || cc["type"] != "ephemeral" {
				t.Fatalf("cache_control = %#v", cc)
			}
			messages := body["messages"].([]map[string]any)
			parts := messages[0]["content"].([]map[string]any)
			if parts[0]["text"] != systemText {
				t.Fatalf("system text changed: %q", parts[0]["text"])
			}
			if messages[1]["content"] != "hi" {
				t.Fatalf("user message must stay plain string: %#v", messages[1])
			}
		})
	}

	t.Run("unsupported_model", func(t *testing.T) {
		body := p.buildRequestBody("gpt-4.1-mini", systemUserRequest("sys", "hi", caching), false)
		messages := body["messages"].([]map[string]any)
		if messages[0]["content"] != "sys" {
			t.Fatalf("unsupported model must not attach cache_control: %#v", messages[0]["content"])
		}
	})
}

func TestBuildRequestBodyDashScopeExplicitCacheInvariants(t *testing.T) {
	p := dashScopeProvider()

	t.Run("caching_disabled", func(t *testing.T) {
		body := p.buildRequestBody(models.Qwen35Flash, systemUserRequest("sys", "hi", &modelhubv2.CachingConfig{Enabled: false}), false)
		messages := body["messages"].([]map[string]any)
		if messages[0]["content"] != "sys" {
			t.Fatalf("explicit disabled must keep plain system string: %#v", messages[0]["content"])
		}
	})

	t.Run("non_dashscope_host", func(t *testing.T) {
		other := &Provider{baseURL: "https://api.ominilink.ai/v1"}
		body := other.buildRequestBody(models.Qwen35Flash, systemUserRequest("sys", "hi", &modelhubv2.CachingConfig{Enabled: true}), false)
		messages := body["messages"].([]map[string]any)
		if messages[0]["content"] != "sys" {
			t.Fatalf("non-DashScope must not rewrite system content: %#v", messages[0]["content"])
		}
	})

	t.Run("no_system", func(t *testing.T) {
		body := p.buildRequestBody(models.Qwen35Flash, &modelhubv2.GenerateRequest{
			Input: &modelhubv2.Input{
				Caching: &modelhubv2.CachingConfig{Enabled: true},
				Items: []*modelhubv2.InputItem{{
					Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
						Role:  modelhubv2.Role_ROLE_USER,
						Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "only user"}}},
					}},
				}},
			},
			Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
		}, false)
		messages := body["messages"].([]map[string]any)
		if messages[0]["content"] != "only user" {
			t.Fatalf("user-only request must not be marked: %#v", messages[0])
		}
	})
}

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
	p := &Provider{}
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

func TestBuildRequestBodyOmitsUnspecifiedThinking(t *testing.T) {
	p := &Provider{}
	body := p.buildRequestBody(models.Qwen35Flash, &modelhubv2.GenerateRequest{
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}, false)
	if _, ok := body["enable_thinking"]; ok {
		t.Fatalf("unspecified thinking must preserve provider default: %#v", body["enable_thinking"])
	}
}

func TestBuildRequestBodySendsEnabledThinking(t *testing.T) {
	p := &Provider{}
	body := p.buildRequestBody(models.Qwen35Flash, &modelhubv2.GenerateRequest{
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{
			Thinking: modelhubv2.ThinkingMode_THINKING_MODE_ENABLED,
		}}},
	}, false)
	if thinking, ok := body["enable_thinking"].(bool); !ok || !thinking {
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
