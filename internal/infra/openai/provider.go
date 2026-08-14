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

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

const DefaultBaseURL = "https://api.openai.com/v1"

// Provider 适配任意 OpenAI-compatible /chat/completions 端点。
type Provider struct {
	name               string
	apiKey             string
	baseURL            string
	sendEnableThinking bool
	client             *http.Client
}

func New(name, apiKey, baseURL string, sendEnableThinking bool) (*Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" API key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	return &Provider{
		name:               name,
		apiKey:             apiKey,
		baseURL:            strings.TrimRight(baseURL, "/"),
		sendEnableThinking: sendEnableThinking,
		client:             telemetry.NewHTTPClient(),
	}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest) (*modelhubv1.GenerateTextResponse, error) {
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

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest, emit provider.EmitTextEvent) (*modelhubv1.GenerateTextResponse, error) {
	body := p.buildRequestBody(model, request, true)
	respBody, err := p.doRequest(ctx, body)
	if err != nil {
		return nil, err
	}
	defer respBody.Close()

	var fullContent strings.Builder
	toolCallAccum := map[int]*modelhubv1.ToolCall{}
	var finishReason string
	var responseID string
	var usage *modelhubv1.Usage

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
		if delta.Content != "" {
			fullContent.WriteString(delta.Content)
			if emit != nil {
				if err := emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_TextChunk{TextChunk: delta.Content}}); err != nil {
					return nil, err
				}
			}
		}
		for _, tc := range delta.ToolCalls {
			acc, ok := toolCallAccum[tc.Index]
			if !ok {
				acc = &modelhubv1.ToolCall{}
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
	var toolCalls []*modelhubv1.ToolCall
	indexes := make([]int, 0, len(toolCallAccum))
	for index := range toolCallAccum {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		tc := toolCallAccum[i]
		toolCalls = append(toolCalls, tc)
		if emit != nil {
			if err := emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_ToolCall{ToolCall: tc}}); err != nil {
				return nil, err
			}
		}
	}
	response := &modelhubv1.GenerateTextResponse{
		Content:      fullContent.String(),
		ToolCalls:    toolCalls,
		ResponseId:   responseID,
		FinishReason: finishReason,
		Usage:        usage,
	}
	return response, nil
}

func (p *Provider) buildRequestBody(model string, request *modelhubv1.GenerateTextRequest, stream bool) map[string]any {
	body := map[string]any{
		"model":  model,
		"stream": stream,
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	var messages []map[string]any
	if request.Instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": request.Instructions})
	}
	for _, msg := range request.Messages {
		messages = append(messages, convertMessage(msg))
	}
	if !hasToolResultMessage(request.Messages) {
		for _, output := range request.ToolOutputs {
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": output.ToolCallId,
				"content":      output.Output,
			})
		}
	}
	body["messages"] = messages
	if request.MaxOutputTokens != nil {
		body["max_tokens"] = *request.MaxOutputTokens
	}
	// optional 已设置时必须原样下发，否则显式 temperature=0 会被误判为“未配置”。
	if request.Temperature != nil {
		body["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		body["top_p"] = *request.TopP
	}
	if format := request.ResponseFormat; format != nil {
		switch format.Type {
		case modelhubv1.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_OBJECT:
			body["response_format"] = map[string]any{"type": "json_object"}
		case modelhubv1.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA:
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
	if len(request.Tools) > 0 {
		var tools []map[string]any
		for _, t := range request.Tools {
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
	if choice := request.ToolChoice; choice != nil {
		switch choice.Mode {
		case modelhubv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE:
			body["tool_choice"] = "none"
		case modelhubv1.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO:
			body["tool_choice"] = "auto"
		case modelhubv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED:
			body["tool_choice"] = "required"
		case modelhubv1.ToolChoiceMode_TOOL_CHOICE_MODE_FUNCTION:
			body["tool_choice"] = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": choice.FunctionName},
			}
		}
	}
	if p.sendEnableThinking {
		// DashScope 等兼容端用该字段关闭思考链；未配置时不得误加，以免破坏普通 OpenAI。
		body["enable_thinking"] = request.Thinking == modelhubv1.ThinkingMode_THINKING_MODE_ENABLED
	}
	return body
}

func hasToolResultMessage(messages []*modelhubv1.Message) bool {
	for _, msg := range messages {
		if msg.Role == modelhubv1.Role_ROLE_TOOL {
			return true
		}
	}
	return false
}

func convertMessage(msg *modelhubv1.Message) map[string]any {
	m := map[string]any{"role": roleString(msg.Role)}
	if msg.Role == modelhubv1.Role_ROLE_ASSISTANT && len(msg.ToolCalls) > 0 {
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
	if msg.Role == modelhubv1.Role_ROLE_TOOL {
		m["tool_call_id"] = msg.ToolCallId
		m["content"] = messageText(msg)
		return m
	}
	hasMedia := false
	for _, part := range msg.Parts {
		if _, ok := part.Content.(*modelhubv1.ContentPart_Text); !ok {
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
		case *modelhubv1.ContentPart_Text:
			if value.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": value.Text})
			}
		case *modelhubv1.ContentPart_Image:
			if url := mediaURL(value.Image); url != "" {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			}
		}
	}
	m["content"] = parts
	return m
}

func mediaURL(media *modelhubv1.Media) string {
	if media == nil {
		return ""
	}
	switch source := media.Source.(type) {
	case *modelhubv1.Media_Uri:
		return source.Uri
	case *modelhubv1.Media_Data:
		if len(source.Data) == 0 || media.MimeType == "" {
			return ""
		}
		return fmt.Sprintf("data:%s;base64,%s", media.MimeType, base64.StdEncoding.EncodeToString(source.Data))
	default:
		return ""
	}
}

func messageText(message *modelhubv1.Message) string {
	var text strings.Builder
	for _, part := range message.Parts {
		if value, ok := part.Content.(*modelhubv1.ContentPart_Text); ok {
			text.WriteString(value.Text)
		}
	}
	return text.String()
}

func roleString(role modelhubv1.Role) string {
	switch role {
	case modelhubv1.Role_ROLE_SYSTEM:
		return "system"
	case modelhubv1.Role_ROLE_ASSISTANT:
		return "assistant"
	case modelhubv1.Role_ROLE_TOOL:
		return "tool"
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

func convertResponse(resp *chatCompletionResponse) *modelhubv1.GenerateTextResponse {
	if resp == nil || len(resp.Choices) == 0 {
		return &modelhubv1.GenerateTextResponse{}
	}
	choice := resp.Choices[0]
	result := &modelhubv1.GenerateTextResponse{
		Content:      choice.Message.Content,
		ResponseId:   resp.ID,
		FinishReason: choice.FinishReason,
		Usage:        convertUsage(resp.Usage),
	}
	for _, tc := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, &modelhubv1.ToolCall{
			Id:            tc.ID,
			Name:          tc.Function.Name,
			ArgumentsJson: []byte(tc.Function.Arguments),
		})
	}
	return result
}

func convertUsage(u *apiUsage) *modelhubv1.Usage {
	if u == nil {
		return nil
	}
	usage := &modelhubv1.Usage{
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
