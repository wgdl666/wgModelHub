package genai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/internal/thinking"
	"github.com/wgdl666/wgModelHub/models"
	genaisdk "google.golang.org/genai"
)

// 浏览态 Session 级前缀默认缓存 1 小时；Hub 可按 ttl_seconds 覆盖。
const defaultCachedContentTTL = time.Hour

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

// CreateCachedContent 把 system + tools 落到 Gemini CachedContent，供后续 Generate 引用。
func (p *Provider) CreateCachedContent(ctx context.Context, model string, request *modelhubv2.CreateCachedContentRequest) (*modelhubv2.CreateCachedContentResponse, error) {
	if request == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, p.name+" create cached content request is required")
	}
	systemText := strings.TrimSpace(request.GetSystemInstruction())
	tools := buildTools(request.GetTools())
	if systemText == "" && len(tools) == 0 {
		return nil, provider.New(provider.ErrorInvalidArgument, p.name+" cached content requires system_instruction or tools")
	}
	ttl := defaultCachedContentTTL
	if request.GetTtlSeconds() > 0 {
		ttl = time.Duration(request.GetTtlSeconds()) * time.Second
	}
	cfg := &genaisdk.CreateCachedContentConfig{
		TTL:         ttl,
		DisplayName: strings.TrimSpace(request.GetDisplayName()),
		Tools:       tools,
	}
	if systemText != "" {
		cfg.SystemInstruction = genaisdk.NewContentFromText(systemText, genaisdk.RoleUser)
	}
	if len(tools) > 0 {
		// 与后续 Generate 默认 auto 对齐，避免 cache 内外 toolConfig 不一致。
		cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{Mode: genaisdk.FunctionCallingConfigModeAuto}}
	}
	cached, err := p.client.Caches.Create(ctx, model, cfg)
	if err != nil {
		return nil, p.mapError(ctx, "create cached content", err)
	}
	if cached == nil || strings.TrimSpace(cached.Name) == "" {
		return nil, provider.New(provider.ErrorInvalidResponse, p.name+" create cached content returned empty name")
	}
	resp := &modelhubv2.CreateCachedContentResponse{CachedContent: cached.Name}
	if !cached.ExpireTime.IsZero() {
		resp.ExpireAtUnix = cached.ExpireTime.Unix()
	}
	return resp, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if err := validateGeminiGenerateInput(request); err != nil {
		return nil, err
	}
	contents := p.buildContents(request)
	// SYSTEM 已抽到 SystemInstruction；出站前按转换后 contents 校验，避免空 contents 被上游 400 冒充成供应商故障。
	if err := requireGeminiContents(contents); err != nil {
		return nil, err
	}
	response, err := p.client.Models.GenerateContent(ctx, model, contents, p.buildConfig(model, request))
	if err != nil {
		return nil, p.mapError(ctx, "generate content", err)
	}
	return convertResponse(response), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	if err := validateGeminiGenerateInput(request); err != nil {
		return nil, err
	}
	contents := p.buildContents(request)
	// 与非流式同一边界：空 contents 在进 SDK 前失败，流式也不会发起供应商连接。
	if err := requireGeminiContents(contents); err != nil {
		return nil, err
	}
	var finishReason string
	var responseID string
	var usage *modelhubv2.Usage

	for response, err := range p.client.Models.GenerateContentStream(ctx, model, contents, p.buildConfig(model, request)) {
		if err != nil {
			return nil, p.mapError(ctx, "stream content", err)
		}
		for _, candidate := range response.Candidates {
			if candidate.Content == nil {
				continue
			}
			for _, part := range candidate.Content.Parts {
				if part.Text != "" && emit != nil {
					if err := emit(provider.TextDeltaEvent(part.Text)); err != nil {
						return nil, err
					}
				}
				if part.FunctionCall == nil {
					continue
				}
				call := convertFunctionCall(part)
				// Gemini 可能在同一轮合法调用同名函数多次；没有稳定 call ID 时不能按名称去重。
				if emit != nil {
					if err := emit(provider.ToolCallEvent(call)); err != nil {
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

	return provider.MetadataFinalEvent(responseID, finishReason, usage), nil
}

func (p *Provider) buildContents(request *modelhubv2.GenerateRequest) []*genaisdk.Content {
	input := request.GetInput()
	if input == nil {
		return nil
	}
	// Input.items 单遍转换；SYSTEM 只进 SystemInstruction，不进入 contents。
	var contents []*genaisdk.Content
	for _, item := range input.GetItems() {
		switch value := item.GetItem().(type) {
		case *modelhubv2.InputItem_Message:
			if value.Message.GetRole() == modelhubv2.Role_ROLE_SYSTEM {
				continue
			}
			if content := convertMessage(value.Message); content != nil {
				contents = append(contents, content)
			}
		case *modelhubv2.InputItem_ToolOutput:
			contents = append(contents, convertToolOutputContents(value.ToolOutput)...)
		}
	}
	return contents
}

// requireGeminiContents 保证至少一条真正进入 Gemini contents 的内容。
// 不能只数非 SYSTEM role：空文本 USER、无 parts 的消息转换后同样为空，上游会 400。
// CachedContent 只是前缀资源，仍需要本轮非 system contents；本校验不改写角色或补占位。
func requireGeminiContents(contents []*genaisdk.Content) error {
	if len(contents) == 0 {
		return provider.New(provider.ErrorInvalidArgument, "Gemini requires at least one non-system content message")
	}
	return nil
}

// convertToolOutputContents 把工具回执转成 function response，图片紧跟该项。
func convertToolOutputContents(output *modelhubv2.ToolOutput) []*genaisdk.Content {
	if output == nil {
		return nil
	}
	name := output.ToolName
	if name == "" {
		name = output.ToolCallId
	}
	contents := []*genaisdk.Content{genaisdk.NewContentFromFunctionResponse(name, toolOutputObject(output), genaisdk.RoleUser)}
	if len(output.Images) == 0 {
		return contents
	}
	parts := make([]*genaisdk.Part, 0, len(output.Images))
	for _, image := range output.Images {
		if part := convertMedia(image); part != nil {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return contents
	}
	return append(contents, genaisdk.NewContentFromParts(parts, genaisdk.RoleUser))
}

func convertMessage(message *modelhubv2.Message) *genaisdk.Content {
	role := genaisdk.Role(genaisdk.RoleUser)
	if message.Role == modelhubv2.Role_ROLE_ASSISTANT {
		role = genaisdk.Role(genaisdk.RoleModel)
	}
	// 文本 parts 与 tool_calls 可同属一条 assistant Message；二者都保留，先文本后工具调用。
	parts := make([]*genaisdk.Part, 0, len(message.Parts)+len(message.ToolCalls))
	for _, part := range message.Parts {
		if converted := convertContentPart(part); converted != nil {
			parts = append(parts, converted)
		}
	}
	if message.Role == modelhubv2.Role_ROLE_ASSISTANT {
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
	}
	if len(parts) == 0 {
		return nil
	}
	return genaisdk.NewContentFromParts(parts, role)
}

func convertContentPart(part *modelhubv2.ContentPart) *genaisdk.Part {
	switch value := part.Content.(type) {
	case *modelhubv2.ContentPart_Text:
		if value.Text == "" {
			return nil
		}
		return genaisdk.NewPartFromText(value.Text)
	case *modelhubv2.ContentPart_Image:
		return convertMedia(value.Image)
	case *modelhubv2.ContentPart_Video:
		return convertMedia(value.Video)
	case *modelhubv2.ContentPart_Audio:
		return convertMedia(value.Audio)
	case *modelhubv2.ContentPart_File:
		return convertMedia(value.File)
	default:
		return nil
	}
}

func convertMedia(media *modelhubv2.Media) *genaisdk.Part {
	if media == nil || strings.TrimSpace(media.MimeType) == "" {
		return nil
	}
	switch source := media.Source.(type) {
	case *modelhubv2.Media_Data:
		if len(source.Data) == 0 {
			return nil
		}
		return genaisdk.NewPartFromBytes(source.Data, media.MimeType)
	case *modelhubv2.Media_Uri:
		if strings.TrimSpace(source.Uri) == "" {
			return nil
		}
		return genaisdk.NewPartFromURI(source.Uri, media.MimeType)
	default:
		return nil
	}
}

func (p *Provider) buildConfig(model string, request *modelhubv2.GenerateRequest) *genaisdk.GenerateContentConfig {
	input := request.GetInput()
	if input == nil {
		input = &modelhubv2.Input{}
	}
	textSpec := request.GetOutput().GetText()
	cfg := &genaisdk.GenerateContentConfig{}
	cachedContent := strings.TrimSpace(input.GetCachedContent())
	if cachedContent != "" {
		// 显式 cache 已含 system/tools；再下发会与资源冲突或拆掉前缀命中。
		cfg.CachedContent = cachedContent
	} else {
		// 仅拼接前置连续 SYSTEM 文本进 SystemInstruction；中途 SYSTEM 不造复杂状态机。
		var systemTexts []string
		for _, item := range input.GetItems() {
			message := item.GetMessage()
			if message == nil || message.Role != modelhubv2.Role_ROLE_SYSTEM {
				break
			}
			if text := strings.TrimSpace(messageText(message)); text != "" {
				systemTexts = append(systemTexts, text)
			}
		}
		if len(systemTexts) > 0 {
			cfg.SystemInstruction = genaisdk.NewContentFromText(strings.Join(systemTexts, "\n"), genaisdk.RoleUser)
		}
	}
	if textSpec != nil && textSpec.MaxOutputTokens != nil {
		cfg.MaxOutputTokens = *textSpec.MaxOutputTokens
	}
	if textSpec != nil && textSpec.Temperature != nil {
		value := float32(*textSpec.Temperature)
		cfg.Temperature = &value
	}
	if textSpec != nil && textSpec.TopP != nil {
		value := float32(*textSpec.TopP)
		cfg.TopP = &value
	}
	applyTextThinking(cfg, model, textSpec)
	if textSpec != nil {
		if format := textSpec.ResponseFormat; format != nil {
			switch format.Type {
			case modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_OBJECT:
				cfg.ResponseMIMEType = "application/json"
			case modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA:
				cfg.ResponseMIMEType = "application/json"
				if len(format.JsonSchema) > 0 {
					var schema any
					_ = json.Unmarshal(format.JsonSchema, &schema)
					cfg.ResponseJsonSchema = schema
				}
			}
		}
	}
	if cachedContent == "" {
		cfg.Tools = buildTools(input.GetTools())
		if len(cfg.Tools) > 0 {
			cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{Mode: genaisdk.FunctionCallingConfigModeAuto}}
		}
	}
	if choice := input.GetToolChoice(); choice != nil {
		switch choice.Mode {
		case modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_NONE:
			cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{Mode: genaisdk.FunctionCallingConfigModeNone}}
		case modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED:
			cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{Mode: genaisdk.FunctionCallingConfigModeAny}}
		case modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_FUNCTION:
			cfg.ToolConfig = &genaisdk.ToolConfig{FunctionCallingConfig: &genaisdk.FunctionCallingConfig{
				Mode:                 genaisdk.FunctionCallingConfigModeAny,
				AllowedFunctionNames: []string{choice.FunctionName},
			}}
		}
	}
	// 保持迁移前行为：业务内容安全判定仍由 Hub/Wardrobe 自己负责。
	cfg.SafetySettings = []*genaisdk.SafetySetting{
		{Category: genaisdk.HarmCategoryHarassment, Threshold: genaisdk.HarmBlockThresholdBlockNone},
		{Category: genaisdk.HarmCategoryHateSpeech, Threshold: genaisdk.HarmBlockThresholdBlockNone},
		{Category: genaisdk.HarmCategorySexuallyExplicit, Threshold: genaisdk.HarmBlockThresholdBlockNone},
		{Category: genaisdk.HarmCategoryDangerousContent, Threshold: genaisdk.HarmBlockThresholdBlockNone},
	}
	return cfg
}

// gemini25BudgetMin / gemini25BudgetMax 是官方 thinkingBudget 范围。超出范围夹紧，避免非法数字让请求失败。
const (
	gemini25BudgetMin int32 = 0
	gemini25BudgetMax int32 = 24576
)

// applyTextThinking 按模型只发一种思考参数。
// 3.7/3.8 与 3.5-flash-lite 认档位，预算丢掉；2.5 只认数字，有预算用预算，否则把档位换成数字。
// 2.0 没有思考参数，三个字段都不发。
func applyTextThinking(cfg *genaisdk.GenerateContentConfig, model string, textSpec *modelhubv2.TextOutput) {
	if cfg == nil || textSpec == nil {
		return
	}
	choice := thinking.FromText(textSpec)
	switch model {
	case models.Gemini37Flash, models.Gemini38Flash:
		// 3.7/3.8 只有 LOW/MEDIUM/HIGH，MINIMAL 和关都会 400 或关不掉，统一落到 LOW。
		if choice.Off {
			cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingLevel: genaisdk.ThinkingLevelLow}
			return
		}
		if choice.Level != modelhubv2.ThinkingLevel_THINKING_LEVEL_UNSPECIFIED {
			cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingLevel: geminiTextLevel(choice.Level, false)}
		}
	case models.Gemini35FlashLite:
		if choice.Off {
			cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingLevel: genaisdk.ThinkingLevelMinimal}
			return
		}
		if choice.Level != modelhubv2.ThinkingLevel_THINKING_LEVEL_UNSPECIFIED {
			cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingLevel: geminiTextLevel(choice.Level, true)}
		}
	case models.Gemini25Flash:
		applyGemini25Budget(cfg, choice)
	case models.Gemini20Flash001:
		// 2.0 的思考文档没有开关、档位或预算，原样转发会变成未文档化字段。
	}
}

func applyGemini25Budget(cfg *genaisdk.GenerateContentConfig, choice thinking.Choice) {
	switch {
	case choice.Off:
		cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingBudget: genaisdk.Ptr(int32(0))}
	case choice.Budget != nil:
		budget := clampGemini25Budget(*choice.Budget)
		cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingBudget: &budget}
	case choice.Level != modelhubv2.ThinkingLevel_THINKING_LEVEL_UNSPECIFIED:
		budget := gemini25BudgetForLevel(choice.Level)
		cfg.ThinkingConfig = &genaisdk.ThinkingConfig{ThinkingBudget: &budget}
	}
}

func clampGemini25Budget(budget int32) int32 {
	if budget < gemini25BudgetMin {
		return gemini25BudgetMin
	}
	if budget > gemini25BudgetMax {
		return gemini25BudgetMax
	}
	return budget
}

// gemini25BudgetForLevel 里的 1024 不是官方档位表，只是合法范围内的偏低数字。
// 0、8192、24576 分别对应文档里的关闭、默认上限和最大预算。
func gemini25BudgetForLevel(level modelhubv2.ThinkingLevel) int32 {
	switch level {
	case modelhubv2.ThinkingLevel_THINKING_LEVEL_LOW:
		return 1024
	case modelhubv2.ThinkingLevel_THINKING_LEVEL_MEDIUM:
		return 8192
	case modelhubv2.ThinkingLevel_THINKING_LEVEL_HIGH:
		return gemini25BudgetMax
	default:
		return 0
	}
}

func geminiTextLevel(level modelhubv2.ThinkingLevel, minimalOK bool) genaisdk.ThinkingLevel {
	converted := convertImageThinkingLevel(level)
	if !minimalOK && converted == genaisdk.ThinkingLevelMinimal {
		return genaisdk.ThinkingLevelLow
	}
	return converted
}

func buildTools(tools []*modelhubv2.Tool) []*genaisdk.Tool {
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
			declaration.ParametersJsonSchema = sanitizeGeminiToolSchema(schema)
		}
		declarations = append(declarations, declaration)
	}
	if len(declarations) == 0 {
		return nil
	}
	return []*genaisdk.Tool{{FunctionDeclarations: declarations}}
}

func convertResponse(response *genaisdk.GenerateContentResponse) *modelhubv2.GenerateEvent {
	if response == nil {
		return provider.TextFinalEvent("", nil, "", "", nil)
	}
	var content strings.Builder
	var toolCalls []*modelhubv2.ToolCall
	var finishReason string
	for _, candidate := range response.Candidates {
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			content.WriteString(part.Text)
			if part.FunctionCall != nil {
				toolCalls = append(toolCalls, convertFunctionCall(part))
			}
		}
		if finishReason == "" && candidate.FinishReason != "" {
			finishReason = string(candidate.FinishReason)
		}
	}
	return provider.TextFinalEvent(content.String(), toolCalls, response.ResponseID, finishReason, convertUsage(response.UsageMetadata))
}

func convertFunctionCall(part *genaisdk.Part) *modelhubv2.ToolCall {
	call := part.FunctionCall
	arguments, _ := json.Marshal(call.Args)
	id := call.ID
	if id == "" {
		id = call.Name
	}
	return &modelhubv2.ToolCall{
		Id:               id,
		Name:             call.Name,
		ArgumentsJson:    arguments,
		ThoughtSignature: part.ThoughtSignature,
	}
}

func convertUsage(metadata *genaisdk.GenerateContentResponseUsageMetadata) *modelhubv2.Usage {
	if metadata == nil {
		return nil
	}
	usage := &modelhubv2.Usage{
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

func messageText(message *modelhubv2.Message) string {
	var text strings.Builder
	for _, part := range message.Parts {
		if value, ok := part.Content.(*modelhubv2.ContentPart_Text); ok {
			text.WriteString(value.Text)
		}
	}
	return text.String()
}

func toolOutputObject(output *modelhubv2.ToolOutput) map[string]any {
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
		// Gemini 拒因在 error.message / error.status，不能只留 HTTP 状态码。
		return provider.FromHTTPDetail(p.name, apiError.Code, strings.TrimSpace(apiError.Status+" "+apiError.Message))
	}
	return provider.Wrap(provider.ErrorUnavailable, p.name+" "+operation+" failed", err)
}
