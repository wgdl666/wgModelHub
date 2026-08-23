package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

const DefaultBaseURL = "https://api.openai.com/v1"

// Provider 适配 OpenAI-compatible 端点：文本走 /chat/completions，生图按有无参考图走 /images/generations 或 /images/edits。
type Provider struct {
	name    string
	apiKey  string
	baseURL string
	client  *http.Client
}

func New(name, apiKey, baseURL string) (*Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" API key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	return &Provider{
		name:    name,
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  telemetry.NewHTTPClient(),
	}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	body := p.buildRequestBody(model, request, false)
	respBody, err := p.doRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	defer respBody.Close()

	var chatResp chatCompletionResponse
	if err := json.NewDecoder(respBody).Decode(&chatResp); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode failed", err)
	}
	return convertResponse(&chatResp), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	body := p.buildRequestBody(model, request, true)
	respBody, err := p.doRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	defer respBody.Close()

	toolCallAccum := map[int]*modelhubv2.ToolCall{}
	var finishReason string
	var responseID string
	var usage *modelhubv2.Usage

	scanner := bufio.NewScanner(respBody)
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxRPCMessageBytes)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" stream chunk is invalid", err)
		}
		if chunk.ID != "" {
			responseID = chunk.ID
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				usage = convertUsage(chunk.Usage)
			}
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" && emit != nil {
			if err := emit(provider.TextDeltaEvent(delta.Content)); err != nil {
				return nil, err
			}
		}
		for _, tc := range delta.ToolCalls {
			acc, ok := toolCallAccum[tc.Index]
			if !ok {
				acc = &modelhubv2.ToolCall{}
				toolCallAccum[tc.Index] = acc
			}
			if tc.ID != "" {
				acc.Id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.Name = tc.Function.Name
			}
			acc.ArgumentsJson = append(acc.ArgumentsJson, []byte(tc.Function.Arguments)...)
		}
		if chunk.Choices[0].FinishReason != "" {
			finishReason = chunk.Choices[0].FinishReason
		}
		if chunk.Usage != nil {
			usage = convertUsage(chunk.Usage)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" stream read failed", err)
	}

	// 流式增量结束后再按 index 顺序发出完整 tool call，避免半成品重复事件。
	indexes := make([]int, 0, len(toolCallAccum))
	for index := range toolCallAccum {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		if emit != nil {
			if err := emit(provider.ToolCallEvent(toolCallAccum[i])); err != nil {
				return nil, err
			}
		}
	}
	return provider.MetadataFinalEvent(responseID, finishReason, usage), nil
}

func (p *Provider) buildRequestBody(model string, request *modelhubv2.GenerateRequest, stream bool) map[string]any {
	input := request.GetInput()
	if input == nil {
		input = &modelhubv2.Input{}
	}
	text := request.GetOutput().GetText()
	body := map[string]any{
		"model":  model,
		"stream": stream,
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	// Input.items 单遍转换，保留 Message/ToolOutput 交错顺序；ToolOutput 图片紧跟该项。
	var messages []map[string]any
	for _, item := range input.GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.InputItem_Message:
			messages = append(messages, convertMessage(value.Message))
		case *modelhubv2.InputItem_ToolOutput:
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": value.ToolOutput.GetToolCallId(),
				"content":      value.ToolOutput.GetOutput(),
			})
			for _, image := range value.ToolOutput.GetImages() {
				if url := mediaURL(image); url != "" {
					messages = append(messages, map[string]any{
						"role": "user",
						"content": []map[string]any{{
							"type":      "image_url",
							"image_url": map[string]any{"url": url},
						}},
					})
				}
			}
		}
	}
	body["messages"] = messages
	if text != nil && text.MaxOutputTokens != nil {
		body["max_tokens"] = *text.MaxOutputTokens
	}
	// optional 已设置时必须原样下发，否则显式 temperature=0 会被误判为“未配置”。
	if text != nil && text.Temperature != nil {
		body["temperature"] = *text.Temperature
	}
	if text != nil && text.TopP != nil {
		body["top_p"] = *text.TopP
	}
	if text != nil {
		if format := text.ResponseFormat; format != nil {
			switch format.Type {
			case modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_OBJECT:
				body["response_format"] = map[string]any{"type": "json_object"}
			case modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA:
				rf := map[string]any{
					"type": "json_schema",
					"json_schema": map[string]any{
						"name":   format.Name,
						"strict": true,
					},
				}
				if len(format.JsonSchema) > 0 {
					var schema any
					_ = json.Unmarshal(format.JsonSchema, &schema)
					rf["json_schema"].(map[string]any)["schema"] = schema
				}
				body["response_format"] = rf
			}
		}
	}
	if len(input.GetTools()) > 0 {
		var tools []map[string]any
		for _, t := range input.GetTools() {
			if t == nil || t.Function == nil {
				continue
			}
			tool := map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Function.Name,
					"description": t.Function.Description,
				},
			}
			if len(t.Function.ParametersJsonSchema) > 0 {
				var params any
				_ = json.Unmarshal(t.Function.ParametersJsonSchema, &params)
				tool["function"].(map[string]any)["parameters"] = params
			}
			tools = append(tools, tool)
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
	}
	if choice := input.GetToolChoice(); choice != nil {
		switch choice.Mode {
		case modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_NONE:
			body["tool_choice"] = "none"
		case modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO:
			body["tool_choice"] = "auto"
		case modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED:
			body["tool_choice"] = "required"
		case modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_FUNCTION:
			body["tool_choice"] = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": choice.FunctionName},
			}
		}
	}
	if text != nil && text.Thinking != modelhubv2.ThinkingMode_THINKING_MODE_UNSPECIFIED {
		// thinking 是统一协议语义：显式启停必须下发，UNSPECIFIED 才保留供应商默认行为。
		body["enable_thinking"] = text.Thinking == modelhubv2.ThinkingMode_THINKING_MODE_ENABLED
	}
	return body
}

func convertMessage(msg *modelhubv2.Message) map[string]any {
	m := map[string]any{"role": roleString(msg.Role)}
	if msg.Role == modelhubv2.Role_ROLE_ASSISTANT && len(msg.ToolCalls) > 0 {
		var tcs []map[string]any
		for _, tc := range msg.ToolCalls {
			tcs = append(tcs, map[string]any{
				"id":   tc.Id,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": string(tc.ArgumentsJson),
				},
			})
		}
		m["tool_calls"] = tcs
		if text := messageText(msg); text != "" {
			m["content"] = text
		}
		return m
	}
	hasMedia := false
	for _, part := range msg.Parts {
		if _, ok := part.Content.(*modelhubv2.ContentPart_Text); !ok {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		m["content"] = messageText(msg)
		return m
	}
	var parts []map[string]any
	for _, part := range msg.Parts {
		switch value := part.Content.(type) {
		case *modelhubv2.ContentPart_Text:
			if value.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": value.Text})
			}
		case *modelhubv2.ContentPart_Image:
			if url := mediaURL(value.Image); url != "" {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			}
		}
	}
	m["content"] = parts
	return m
}

func mediaURL(media *modelhubv2.Media) string {
	if media == nil {
		return ""
	}
	switch source := media.Source.(type) {
	case *modelhubv2.Media_Uri:
		return source.Uri
	case *modelhubv2.Media_Data:
		if len(source.Data) == 0 || media.MimeType == "" {
			return ""
		}
		return fmt.Sprintf("data:%s;base64,%s", media.MimeType, base64.StdEncoding.EncodeToString(source.Data))
	default:
		return ""
	}
}

func messageText(message *modelhubv2.Message) string {
	var text strings.Builder
	for _, part := range message.Parts {
		if value, ok := part.Content.(*modelhubv2.ContentPart_Text); ok {
			text.WriteString(value.Text)
		}
	}
	return text.String()
}

func roleString(role modelhubv2.Role) string {
	switch role {
	case modelhubv2.Role_ROLE_SYSTEM:
		return "system"
	case modelhubv2.Role_ROLE_ASSISTANT:
		return "assistant"
	default:
		return "user"
	}
}

func (p *Provider) doRequest(ctx context.Context, body map[string]any) (io.ReadCloser, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" marshal failed", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" create request failed", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" request failed", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	return resp.Body, nil
}

type chatCompletionResponse struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *apiUsage    `json:"usage,omitempty"`
}

type chatCompletionChunk struct {
	ID      string            `json:"id"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *apiUsage         `json:"usage,omitempty"`
}

type chatChoice struct {
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatChunkChoice struct {
	Delta        chatDelta `json:"delta"`
	FinishReason string    `json:"finish_reason"`
}

type chatMessage struct {
	Content   string        `json:"content"`
	ToolCalls []apiToolCall `json:"tool_calls,omitempty"`
}

type chatDelta struct {
	Content   string             `json:"content"`
	ToolCalls []apiToolCallDelta `json:"tool_calls,omitempty"`
}

type apiToolCall struct {
	ID       string      `json:"id"`
	Function apiFunction `json:"function"`
}

type apiToolCallDelta struct {
	Index    int         `json:"index"`
	ID       string      `json:"id,omitempty"`
	Function apiFunction `json:"function"`
}

type apiFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type apiUsage struct {
	PromptTokens            int                         `json:"prompt_tokens"`
	CompletionTokens        int                         `json:"completion_tokens"`
	TotalTokens             int                         `json:"total_tokens"`
	PromptTokensDetails     *apiPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *apiCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type apiPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type apiCompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

func convertResponse(resp *chatCompletionResponse) *modelhubv2.GenerateEvent {
	if resp == nil || len(resp.Choices) == 0 {
		return provider.TextFinalEvent("", nil, "", "", nil)
	}
	choice := resp.Choices[0]
	var toolCalls []*modelhubv2.ToolCall
	for _, tc := range choice.Message.ToolCalls {
		toolCalls = append(toolCalls, &modelhubv2.ToolCall{
			Id:            tc.ID,
			Name:          tc.Function.Name,
			ArgumentsJson: []byte(tc.Function.Arguments),
		})
	}
	return provider.TextFinalEvent(choice.Message.Content, toolCalls, resp.ID, choice.FinishReason, convertUsage(resp.Usage))
}

func convertUsage(u *apiUsage) *modelhubv2.Usage {
	if u == nil {
		return nil
	}
	usage := &modelhubv2.Usage{
		InputTokens:  int64(u.PromptTokens),
		OutputTokens: int64(u.CompletionTokens),
		TotalTokens:  int64(u.TotalTokens),
	}
	if u.PromptTokensDetails != nil {
		usage.CachedTokens = int64(u.PromptTokensDetails.CachedTokens)
	}
	if u.CompletionTokensDetails != nil {
		usage.ReasoningTokens = int64(u.CompletionTokensDetails.ReasoningTokens)
	}
	return usage
}
