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
	"net/url"
	"strings"

	"github.com/wgdl666/kangaroo/logs"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
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
		// 千问给啥就吐啥：这一包的 id/name/arguments/index 原样下发，不在 ModelHub 拼完整 call。
		// index 按 JSON presence 透传（显式 0 保留，字段缺失则为 nil）；空 arguments 也必须带上同一 index。
		for _, tc := range delta.ToolCalls {
			if emit == nil {
				continue
			}
			out := &modelhubv2.ToolCall{
				Id:            tc.ID,
				Name:          tc.Function.Name,
				ArgumentsJson: []byte(tc.Function.Arguments),
			}
			if tc.Index != nil {
				index := int32(*tc.Index)
				out.Index = &index
			}
			if err := emit(provider.ToolCallEvent(out)); err != nil {
				return nil, err
			}
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
	// DashScope qwen3.5-flash 显式上下文缓存：仅在受信 hostname、显式 enabled 且存在带文本的 system 消息时标记稳定前缀。
	if p.dashScopeExplicitCacheEligible(model, input) {
		applyDashScopeSystemCacheControl(messages)
	}
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
		applyThinking(body, model, text.Thinking)
	}
	return body
}

// applyThinking 把统一协议 ThinkingMode 映射到供应商字段。
// DashScope Qwen 走 enable_thinking。
// GLM-5.3-flash：当前智谱 endpoint 接受 thinking.type=disabled（ToolChoice=required 探针通过）；
// 但仍可能返回 reasoning_content，因此 DISABLED→disabled 不是保证 true-off，只是显式关闭请求。
// Claude Haiku 走 Anthropic OpenAI-compat，不得下发 DashScope/GLM 私有思考字段；
// DISABLED 对 Haiku 表示不启用任何额外思考字段。
func applyThinking(body map[string]any, model string, thinking modelhubv2.ThinkingMode) {
	if thinking == modelhubv2.ThinkingMode_THINKING_MODE_UNSPECIFIED {
		return
	}
	if model == models.ClaudeHaiku45 {
		return
	}
	if model == models.GLM53Flash {
		if thinking == modelhubv2.ThinkingMode_THINKING_MODE_DISABLED {
			body["thinking"] = map[string]any{"type": "disabled"}
			return
		}
		body["thinking"] = map[string]any{"type": "enabled"}
		return
	}
	body["enable_thinking"] = thinking == modelhubv2.ThinkingMode_THINKING_MODE_ENABLED
}

// dashScopeExplicitCacheEligible 判定是否应向 DashScope 下发显式 cache_control。
// 仅 DashScope 官方 host 上已开通显式 ephemeral 缓存的 Qwen 文本模型可携带该字段；
// Claude/OminiLink/OpenAI 默认实例与其它模型不得误标。
func (p *Provider) dashScopeExplicitCacheEligible(model string, input *modelhubv2.Input) bool {
	switch model {
	case models.QwenFlash, models.Qwen37Flash, models.Qwen38Flash, models.Qwen35Flash, models.Qwen3VLPlus:
	default:
		return false
	}
	if input == nil || input.Caching == nil || !input.Caching.Enabled {
		return false
	}
	u, err := url.Parse(p.baseURL)
	if err != nil || u.Hostname() != "dashscope.aliyuncs.com" {
		return false
	}
	return true
}

// applyDashScopeSystemCacheControl 把首条含文本的 system 消息等价转为 content array，并在最后一个 text block 上附加 ephemeral 标记。
// 无 system 文本时不改 user/assistant/tool 消息，避免把非稳定前缀误标为缓存块。
func applyDashScopeSystemCacheControl(messages []map[string]any) {
	for _, msg := range messages {
		if msg["role"] != "system" {
			continue
		}
		switch content := msg["content"].(type) {
		case string:
			if strings.TrimSpace(content) == "" {
				return
			}
			msg["content"] = []map[string]any{{
				"type":          "text",
				"text":          content,
				"cache_control": map[string]any{"type": "ephemeral"},
			}}
			return
		case []map[string]any:
			lastText := -1
			for i, part := range content {
				if part["type"] != "text" {
					continue
				}
				if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
					lastText = i
				}
			}
			if lastText < 0 {
				return
			}
			content[lastText]["cache_control"] = map[string]any{"type": "ephemeral"}
			return
		default:
			return
		}
	}
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
		// 只读供应商拒因，不回读请求体；否则 Hub 只能看到光秃秃的 HTTP 400。
		// 额外附带请求形态诊断（不含 prompt / 原始 arguments），区分历史 arguments 非法 JSON 与供应商侧生成失败。
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		resp.Body.Close()
		detail := strings.TrimSpace(string(raw))
		fields := append([]any{
			"provider", p.name,
			"status", resp.StatusCode,
		}, chatRequestDiagFields(body)...)
		if snippet := provider.CompactHTTPErrorDetail(detail); snippet != "" {
			fields = append(fields, "body", snippet)
		}
		logs.Default().Error("openai_http_error", fields...)
		return nil, provider.FromHTTPDetail(p.name, resp.StatusCode, detail)
	}
	return resp.Body, nil
}

// invalidFunctionArgumentsEntry 仅记录定位非法 arguments 所需的紧凑元数据，绝不包含 arguments 原文。
type invalidFunctionArgumentsEntry struct {
	MessageIndex int    `json:"message_index"`
	ToolIndex    int    `json:"tool_index"`
	ToolName     string `json:"tool_name,omitempty"`
	ArgumentsLen int    `json:"arguments_len"`
}

// chatRequestDiagFields 从即将下发的 chat/completions body 提取安全诊断字段。
// 只统计形态与 JSON 合法性，不读取消息文本、有效 arguments 或密钥。
func chatRequestDiagFields(body map[string]any) []any {
	model, _ := body["model"].(string)
	stream, _ := body["stream"].(bool)
	messages, _ := body["messages"].([]map[string]any)
	tools, _ := body["tools"].([]map[string]any)

	assistantToolCalls := 0
	var invalid []invalidFunctionArgumentsEntry
	for msgIdx, msg := range messages {
		if msg["role"] != "assistant" {
			continue
		}
		tcs, ok := msg["tool_calls"].([]map[string]any)
		if !ok {
			continue
		}
		for toolIdx, tc := range tcs {
			assistantToolCalls++
			fn, _ := tc["function"].(map[string]any)
			args, argsOK := fn["arguments"].(string)
			name, _ := fn["name"].(string)
			if !argsOK || !json.Valid([]byte(args)) {
				entry := invalidFunctionArgumentsEntry{
					MessageIndex: msgIdx,
					ToolIndex:    toolIdx,
					ToolName:     name,
				}
				if argsOK {
					entry.ArgumentsLen = len(args)
				}
				invalid = append(invalid, entry)
			}
		}
	}

	fields := []any{
		"model", model,
		"stream", stream,
		"message_count", len(messages),
		"tool_count", len(tools),
		"assistant_tool_call_count", assistantToolCalls,
		"invalid_function_arguments_count", len(invalid),
	}
	if len(invalid) > 0 {
		fields = append(fields, "invalid_function_arguments", invalid)
	}
	return fields
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
	// *int 保留供应商 JSON 的 index presence：缺字段为 nil，显式 0 为非 nil。
	Index    *int        `json:"index"`
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
