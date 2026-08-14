package genai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	genaisdk "google.golang.org/genai"
)

type Provider struct {
	name   string
	client *genaisdk.Client
}

func NewGemini(ctx context.Context, name, apiKey, endpoint, proxyURL string) (*Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" API key is required")
	}
	cfg := &genaisdk.ClientConfig{
		APIKey:     apiKey,
		Backend:    genaisdk.BackendGeminiAPI,
		HTTPClient: telemetry.NewHTTPClient(),
	}
	if endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/"); endpoint != "" {
		endpoint = strings.TrimSuffix(endpoint, "/v1")
		cfg.HTTPOptions = genaisdk.HTTPOptions{BaseURL: endpoint}
	}
	if proxyURL = strings.TrimSpace(proxyURL); proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorConfiguration, name+" proxy URL is invalid", err)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = http.ProxyURL(parsed)
		cfg.HTTPClient = telemetry.NewHTTPClientWithTransport(transport)
	}
	client, err := genaisdk.NewClient(ctx, cfg)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "create "+name+" client", err)
	}
	return &Provider{name: name, client: client}, nil
}

func NewVertexAI(ctx context.Context, name, project, location string) (*Provider, error) {
	if strings.TrimSpace(project) == "" || strings.TrimSpace(location) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" project and location are required")
	}
	client, err := genaisdk.NewClient(ctx, &genaisdk.ClientConfig{
		Backend:    genaisdk.BackendVertexAI,
		Project:    project,
		Location:   location,
		HTTPClient: telemetry.NewHTTPClient(),
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "create "+name+" client", err)
	}
	return &Provider{name: name, client: client}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest) (*modelhubv1.GenerateTextResponse, error) {
	response, err := p.client.Models.GenerateContent(ctx, model, p.buildContents(request), p.buildConfig(request))
	if err != nil {
		return nil, p.mapError(ctx, "generate content", err)
	}
	return convertResponse(response), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest, emit provider.EmitTextEvent) (*modelhubv1.GenerateTextResponse, error) {
	var content strings.Builder
	var toolCalls []*modelhubv1.ToolCall
	var finishReason string
	var responseID string
	var usage *modelhubv1.Usage

	for response, err := range p.client.Models.GenerateContentStream(ctx, model, p.buildContents(request), p.buildConfig(request)) {
		if err != nil {
			return nil, p.mapError(ctx, "stream content", err)
		}
		for _, candidate := range response.Candidates {
			if candidate.Content == nil {
				continue
			}
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					content.WriteString(part.Text)
					if emit != nil {
						if err := emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_TextChunk{TextChunk: part.Text}}); err != nil {
							return nil, err
						}
					}
				}
				if part.FunctionCall == nil {
					continue
				}
				call := convertFunctionCall(part)
				// Gemini 可能在同一轮合法调用同名函数多次；没有稳定 call ID 时不能按名称去重。
				toolCalls = append(toolCalls, call)
				if emit != nil {
					if err := emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_ToolCall{ToolCall: call}}); err != nil {
						return nil, err
					}
				}
			}
			if candidate.FinishReason != "" {
				finishReason = string(candidate.FinishReason)
			}
		}
		if response.ResponseID != "" {
			responseID = response.ResponseID
		}
		usage = convertUsage(response.UsageMetadata)
	}

	return &modelhubv1.GenerateTextResponse{
		Content:      content.String(),
		ToolCalls:    toolCalls,
		ResponseId:   responseID,
		FinishReason: finishReason,
		Usage:        usage,
	}, nil
}

func (p *Provider) buildContents(request *modelhubv1.GenerateTextRequest) []*genaisdk.Content {
	contents := make([]*genaisdk.Content, 0, len(request.Messages)+len(request.ToolOutputs))
	hasToolMessage := false
	for _, message := range request.Messages {
		if message.Role == modelhubv1.Role_ROLE_SYSTEM {
			continue
		}
		if message.Role == modelhubv1.Role_ROLE_TOOL {
			hasToolMessage = true
		}
		if content := convertMessage(message); content != nil {
			contents = append(contents, content)
		}
	}

	// 旧调用链可能仍把工具结果放在 tool_outputs；只有消息历史未物化结果时才补入，避免重复续轮。
	if !hasToolMessage {
		for _, output := range request.ToolOutputs {
			response := toolOutputObject(output)
			name := output.ToolName
			if name == "" {
				name = output.ToolCallId
			}
			contents = append(contents, genaisdk.NewContentFromFunctionResponse(name, response, genaisdk.RoleUser))
			if len(output.Images) > 0 {
				parts := make([]*genaisdk.Part, 0, len(output.Images))
				for _, image := range output.Images {
					if part := convertMedia(image); part != nil {
						parts = append(parts, part)
					}
				}
				if len(parts) > 0 {
					contents = append(contents, genaisdk.NewContentFromParts(parts, genaisdk.RoleUser))
				}
			}
		}
	}
	return contents
}

func convertMessage(message *modelhubv1.Message) *genaisdk.Content {
	role := genaisdk.Role(genaisdk.RoleUser)
	if message.Role == modelhubv1.Role_ROLE_ASSISTANT {
		role = genaisdk.Role(genaisdk.RoleModel)
	}
	if message.Role == modelhubv1.Role_ROLE_ASSISTANT && len(message.ToolCalls) > 0 {
		parts := make([]*genaisdk.Part, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			arguments := map[string]any{}
			if len(call.ArgumentsJson) > 0 {
				_ = json.Unmarshal(call.ArgumentsJson, &arguments)
			}
			id := call.Id
			if id == "" {
				id = call.Name
			}
			parts = append(parts, &genaisdk.Part{
				FunctionCall: &genaisdk.FunctionCall{
					ID:   id,
					Name: call.Name,
					Args: arguments,
				},
				ThoughtSignature: call.ThoughtSignature,
			})
		}
		return genaisdk.NewContentFromParts(parts, role)
	}
	if message.Role == modelhubv1.Role_ROLE_TOOL {
		response := map[string]any{}
		text := messageText(message)
		_ = json.Unmarshal([]byte(text), &response)
		if len(response) == 0 {
			response["result"] = text
		}
		name := message.ToolName
		if name == "" {
			name = message.ToolCallId
		}
		return genaisdk.NewContentFromFunctionResponse(name, response, genaisdk.RoleUser)
	}
	parts := make([]*genaisdk.Part, 0, len(message.Parts))
	for _, part := range message.Parts {
		if converted := convertContentPart(part); converted != nil {
			parts = append(parts, converted)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return genaisdk.NewContentFromParts(parts, role)
}

func convertContentPart(part *modelhubv1.ContentPart) *genaisdk.Part {
	switch value := part.Content.(type) {
	case *modelhubv1.ContentPart_Text:
		if value.Text == "" {
			return nil
		}
		return genaisdk.NewPartFromText(value.Text)
	case *modelhubv1.ContentPart_Image:
		return convertMedia(value.Image)
	case *modelhubv1.ContentPart_Video:
		return convertMedia(value.Video)
	case *modelhubv1.ContentPart_Audio:
		return convertMedia(value.Audio)
	case *modelhubv1.ContentPart_File:
		return convertMedia(value.File)
	default:
		return nil
	}
}

func convertMedia(media *modelhubv1.Media) *genaisdk.Part {
	if media == nil || strings.TrimSpace(media.MimeType) == "" {
		return nil
	}
	switch source := media.Source.(type) {
	case *modelhubv1.Media_Data:
		if len(source.Data) == 0 {
			return nil
		}
		return genaisdk.NewPartFromBytes(source.Data, media.MimeType)
	case *modelhubv1.Media_Uri:
		if strings.TrimSpace(source.Uri) == "" {
			return nil
		}
		return genaisdk.NewPartFromURI(source.Uri, media.MimeType)
	default:
		return nil
	}
}

func (p *Provider) buildConfig(request *modelhubv1.GenerateTextRequest) *genaisdk.GenerateContentConfig {
	cfg := &genaisdk.GenerateContentConfig{}
	if request.Instructions != "" {
		cfg.SystemInstruction = genaisdk.NewContentFromText(request.Instructions, genaisdk.RoleUser)
	} else {
		for _, message := range request.Messages {
			if message.Role == modelhubv1.Role_ROLE_SYSTEM {
				cfg.SystemInstruction = genaisdk.NewContentFromText(messageText(message), genaisdk.RoleUser)
				break
			}
		}
	}
	if request.MaxOutputTokens != nil {
		cfg.MaxOutputTokens = *request.MaxOutputTokens
	}
	if request.Temperature != nil {
		value := float32(*request.Temperature)
		cfg.Temperature = &value
	}
	if request.TopP != nil {
		value := float32(*request.TopP)
		cfg.TopP = &value
	}
	switch request.Thinking {
	case modelhubv1.ThinkingMode_THINKING_MODE_DISABLED:
		cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingBudget: genaisdk.Ptr(int32(0))}
	case modelhubv1.ThinkingMode_THINKING_MODE_ENABLED:
		cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingBudget: genaisdk.Ptr(int32(1024))}
	}
	if format := request.ResponseFormat; format != nil {
		switch format.Type {
		case modelhubv1.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_OBJECT:
			cfg.ResponseMIMEType = "application/json"
		case modelhubv1.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA:
			cfg.ResponseMIMEType = "application/json"
			if len(format.JsonSchema) > 0 {
				var schema any
				_ = json.Unmarshal(format.JsonSchema, &schema)
				cfg.ResponseJsonSchema = schema
			}
		}
	}
	cfg.Tools = buildTools(request.Tools)
	if len(cfg.Tools) > 0 {
		cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{Mode: genaisdk.FunctionCallingConfigModeAuto}}
	}
	if choice := request.ToolChoice; choice != nil {
		switch choice.Mode {
		case modelhubv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE:
			cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{Mode: genaisdk.FunctionCallingConfigModeNone}}
		case modelhubv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED:
			cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{Mode: genaisdk.FunctionCallingConfigModeAny}}
		case modelhubv1.ToolChoiceMode_TOOL_CHOICE_MODE_FUNCTION:
			cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{
				Mode:                 genaisdk.FunctionCallingConfigModeAny,
				AllowedFunctionNames: []string{choice.FunctionName},
			}}
		}
	}
	// 保持迁移前行为：业务内容安全判定仍由 Hub/Async 自己负责。
	cfg.SafetySettings = []*genaisdk.SafetySetting{
		{Category: genaisdk.HarmCategoryHarassment, Threshold: genaisdk.HarmBlockThresholdBlockNone},
		{Category: genaisdk.HarmCategoryHateSpeech, Threshold: genaisdk.HarmBlockThresholdBlockNone},
		{Category: genaisdk.HarmCategorySexuallyExplicit, Threshold: genaisdk.HarmBlockThresholdBlockNone},
		{Category: genaisdk.HarmCategoryDangerousContent, Threshold: genaisdk.HarmBlockThresholdBlockNone},
	}
	return cfg
}

func buildTools(tools []*modelhubv1.Tool) []*genaisdk.Tool {
	declarations := make([]*genaisdk.FunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || tool.Function == nil || tool.Function.Name == "" {
			continue
		}
		declaration := &genaisdk.FunctionDeclaration{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
		}
		if len(tool.Function.ParametersJsonSchema) > 0 {
			var schema any
			_ = json.Unmarshal(tool.Function.ParametersJsonSchema, &schema)
			declaration.ParametersJsonSchema = schema
		}
		declarations = append(declarations, declaration)
	}
	if len(declarations) == 0 {
		return nil
	}
	return []*genaisdk.Tool{{FunctionDeclarations: declarations}}
}

func convertResponse(response *genaisdk.GenerateContentResponse) *modelhubv1.GenerateTextResponse {
	if response == nil {
		return &modelhubv1.GenerateTextResponse{}
	}
	result := &modelhubv1.GenerateTextResponse{
		ResponseId: response.ResponseID,
		Usage:      convertUsage(response.UsageMetadata),
	}
	var content strings.Builder
	for _, candidate := range response.Candidates {
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			content.WriteString(part.Text)
			if part.FunctionCall != nil {
				result.ToolCalls = append(result.ToolCalls, convertFunctionCall(part))
			}
		}
		if result.FinishReason == "" && candidate.FinishReason != "" {
			result.FinishReason = string(candidate.FinishReason)
		}
	}
	result.Content = content.String()
	return result
}

func convertFunctionCall(part *genaisdk.Part) *modelhubv1.ToolCall {
	call := part.FunctionCall
	arguments, _ := json.Marshal(call.Args)
	id := call.ID
	if id == "" {
		id = call.Name
	}
	return &modelhubv1.ToolCall{
		Id:               id,
		Name:             call.Name,
		ArgumentsJson:    arguments,
		ThoughtSignature: part.ThoughtSignature,
	}
}

func convertUsage(metadata *genaisdk.GenerateContentResponseUsageMetadata) *modelhubv1.Usage {
	if metadata == nil {
		return nil
	}
	usage := &modelhubv1.Usage{
		InputTokens:  int64(metadata.PromptTokenCount),
		OutputTokens: int64(metadata.CandidatesTokenCount),
		TotalTokens:  int64(metadata.TotalTokenCount),
	}
	if metadata.CachedContentTokenCount > 0 {
		usage.CachedTokens = int64(metadata.CachedContentTokenCount)
	} else {
		for _, detail := range metadata.CacheTokensDetails {
			usage.CachedTokens += int64(detail.TokenCount)
		}
	}
	usage.ReasoningTokens = int64(metadata.ThoughtsTokenCount)
	return usage
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

func toolOutputObject(output *modelhubv1.ToolOutput) map[string]any {
	var object map[string]any
	_ = json.Unmarshal([]byte(output.Output), &object)
	if object == nil {
		object = map[string]any{"result": output.Output}
	}
	if output.IsError {
		object["is_error"] = true
	}
	return object
}

func (p *Provider) mapError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiError genaisdk.APIError
	if errors.As(err, &apiError) {
		return provider.FromHTTP(p.name, apiError.Code)
	}
	return provider.Wrap(provider.ErrorUnavailable, p.name+" "+operation+" failed", err)
}
