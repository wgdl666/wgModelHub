package ark

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/volcengine/volcengine-go-sdk/service/arkruntime"
	arkmodel "github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"
	"github.com/volcengine/volcengine-go-sdk/service/arkruntime/model/responses"
	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const DefaultBaseURL = "https://ark.cn-beijing.volces.com/api/v3"

const (
	roleUser      responses.MessageRole_Enum = 1
	roleSystem    responses.MessageRole_Enum = 2
	roleAssistant responses.MessageRole_Enum = 4
)

type Provider struct {
	name   string
	client *arkruntime.Client
}

func New(name, apiKey, baseURL string) (*Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" API key is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	client := arkruntime.NewClientWithApiKey(
		apiKey,
		arkruntime.WithBaseUrl(strings.TrimRight(baseURL, "/")),
	)
	return &Provider{name: name, client: client}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest) (*modelhubv1.GenerateTextResponse, error) {
	arkReq, err := p.buildRequest(model, request)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.CreateResponses(ctx, arkReq)
	if err != nil {
		return nil, p.mapError(ctx, "response failed", err)
	}
	return p.convertResponse(resp), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest, emit provider.EmitTextEvent) (*modelhubv1.GenerateTextResponse, error) {
	arkReq, err := p.buildRequest(model, request)
	if err != nil {
		return nil, err
	}
	arkReq.Stream = ptr(true)

	stream, err := p.client.CreateResponsesStream(ctx, arkReq)
	if err != nil {
		return nil, p.mapError(ctx, "stream failed", err)
	}
	defer stream.Close()

	var content strings.Builder
	var toolCalls []*modelhubv1.ToolCall
	emittedCalls := make(map[string]struct{})
	var finishReason string
	var responseID string
	var usage *modelhubv1.Usage

	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, p.mapError(ctx, "stream recv failed", err)
		}
		if itemEvent := event.GetItemDone(); itemEvent != nil {
			if fc := itemEvent.Item.GetFunctionToolCall(); fc != nil {
				call := &modelhubv1.ToolCall{
					Id:            fc.CallId,
					Name:          fc.Name,
					ArgumentsJson: []byte(fc.Arguments),
				}
				key := call.Id + "\x00" + call.Name
				if _, duplicated := emittedCalls[key]; duplicated {
					continue
				}
				emittedCalls[key] = struct{}{}
				toolCalls = append(toolCalls, call)
				if emit != nil {
					if err := emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_ToolCall{ToolCall: call}}); err != nil {
						return nil, err
					}
				}
			}
		} else if textEvent := event.GetText(); textEvent != nil {
			delta := textEvent.GetDelta()
			if delta != "" {
				content.WriteString(delta)
				if emit != nil {
					if err := emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_TextChunk{TextChunk: delta}}); err != nil {
						return nil, err
					}
				}
			}
		}
		if completed := event.GetResponseCompleted(); completed != nil {
			finishReason = completed.Response.Status.String()
			responseID = completed.Response.Id
			usage = p.extractUsage(completed.Response)
		}
	}

	response := &modelhubv1.GenerateTextResponse{
		Content:      content.String(),
		ToolCalls:    toolCalls,
		ResponseId:   responseID,
		FinishReason: finishReason,
		Usage:        usage,
	}
	return response, nil
}

func (p *Provider) buildRequest(model string, request *modelhubv1.GenerateTextRequest) (*responses.ResponsesRequest, error) {
	arkReq := &responses.ResponsesRequest{
		Model: model,
		Input: p.buildInput(request),
	}
	if request.Instructions != "" {
		arkReq.Instructions = ptr(request.Instructions)
	}
	if request.MaxOutputTokens != nil {
		arkReq.MaxOutputTokens = ptr(int64(*request.MaxOutputTokens))
	}
	// proto optional 已设置时必须原样下发；用 >0 判断会把显式 temperature=0 当成“未配置”而漏传。
	if request.Temperature != nil {
		arkReq.Temperature = ptr(*request.Temperature)
	}
	if request.TopP != nil {
		arkReq.TopP = ptr(*request.TopP)
	}
	if request.PreviousResponseId != "" {
		arkReq.PreviousResponseId = ptr(request.PreviousResponseId)
	}
	if caching := request.Caching; caching != nil && caching.Enabled {
		arkReq.Caching = &responses.ResponsesCaching{Type: responses.CacheType_enabled.Enum()}
		if caching.ExpireAtUnix > 0 {
			arkReq.ExpireAt = ptr(caching.ExpireAtUnix)
		}
	}
	switch request.Thinking {
	case modelhubv1.ThinkingMode_THINKING_MODE_ENABLED:
		arkReq.Thinking = &responses.ResponsesThinking{Type: responses.ThinkingType_enabled.Enum()}
	case modelhubv1.ThinkingMode_THINKING_MODE_DISABLED:
		arkReq.Thinking = &responses.ResponsesThinking{Type: responses.ThinkingType_disabled.Enum()}
	}
	if format := request.ResponseFormat; format != nil {
		switch format.Type {
		case modelhubv1.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_OBJECT:
			arkReq.Text = &responses.ResponsesText{
				Format: &responses.TextFormat{Type: responses.TextType_json_object},
			}
		case modelhubv1.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA:
			arkReq.Text = &responses.ResponsesText{
				Format: &responses.TextFormat{
					Type:   responses.TextType_json_schema,
					Name:   format.Name,
					Schema: &responses.Bytes{Value: format.JsonSchema},
				},
			}
		}
	}
	// 续轮上下文已含工具定义，重复下发会导致 Ark 拒绝或覆盖状态。
	if request.PreviousResponseId == "" && len(request.Tools) > 0 {
		tools, err := p.buildTools(request.Tools)
		if err != nil {
			return nil, err
		}
		arkReq.Tools = tools
	}
	return arkReq, nil
}

func (p *Provider) buildInput(request *modelhubv1.GenerateTextRequest) *responses.ResponsesInput {
	if !hasToolResultMessage(request.Messages) && len(request.ToolOutputs) > 0 {
		var items []*responses.InputItem
		for _, output := range request.ToolOutputs {
			items = append(items, &responses.InputItem{
				Union: &responses.InputItem_FunctionToolCallOutput{
					FunctionToolCallOutput: &responses.ItemFunctionToolCallOutput{
						CallId: output.ToolCallId,
						Output: output.Output,
						Type:   responses.ItemType_function_call_output,
					},
				},
			})
			if len(output.Images) > 0 {
				var contentItems []*responses.ContentItem
				for _, image := range output.Images {
					if url := mediaURL(image); url != "" {
						contentItems = append(contentItems, &responses.ContentItem{
							Union: &responses.ContentItem_Image{
								Image: &responses.ContentItemImage{
									Type:     responses.ContentItemType_input_image,
									ImageUrl: ptr(url),
								},
							},
						})
					}
				}
				if len(contentItems) > 0 {
					items = append(items, &responses.InputItem{
						Union: &responses.InputItem_EasyMessage{
							EasyMessage: &responses.ItemEasyMessage{
								Role: roleUser,
								Content: &responses.MessageContent{
									Union: &responses.MessageContent_ListValue{
										ListValue: &responses.ContentItemList{ListValue: contentItems},
									},
								},
							},
						},
					})
				}
			}
		}
		return &responses.ResponsesInput{
			Union: &responses.ResponsesInput_ListValue{
				ListValue: &responses.InputItemList{ListValue: items},
			},
		}
	}

	if len(request.Messages) == 0 {
		return &responses.ResponsesInput{
			Union: &responses.ResponsesInput_StringValue{StringValue: ""},
		}
	}
	if len(request.Messages) == 1 && request.Messages[0].Role == modelhubv1.Role_ROLE_USER {
		if text := messageText(request.Messages[0]); text != "" && len(request.Messages[0].Parts) == 1 {
			if _, ok := request.Messages[0].Parts[0].Content.(*modelhubv1.ContentPart_Text); ok {
				return &responses.ResponsesInput{
					Union: &responses.ResponsesInput_StringValue{StringValue: text},
				}
			}
		}
	}

	var items []*responses.InputItem
	for _, message := range request.Messages {
		items = append(items, convertMessageToInputItems(message)...)
	}
	return &responses.ResponsesInput{
		Union: &responses.ResponsesInput_ListValue{
			ListValue: &responses.InputItemList{ListValue: items},
		},
	}
}

// convertMessageToInputItems 把一条消息展开为 Ark InputItem；多 tool call 必须各占一项，不能沿用 Hub 只取 [0] 的旧缺陷。
func convertMessageToInputItems(message *modelhubv1.Message) []*responses.InputItem {
	if message == nil {
		return nil
	}
	if message.Role == modelhubv1.Role_ROLE_ASSISTANT && len(message.ToolCalls) > 0 {
		items := make([]*responses.InputItem, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			callID := call.Id
			if callID == "" {
				callID = call.Name
			}
			items = append(items, &responses.InputItem{
				Union: &responses.InputItem_FunctionToolCall{
					FunctionToolCall: &responses.ItemFunctionToolCall{
						Arguments: string(call.ArgumentsJson),
						CallId:    callID,
						Name:      call.Name,
						Type:      responses.ItemType_function_call,
					},
				},
			})
		}
		return items
	}
	if message.Role == modelhubv1.Role_ROLE_TOOL {
		return []*responses.InputItem{{
			Union: &responses.InputItem_FunctionToolCallOutput{
				FunctionToolCallOutput: &responses.ItemFunctionToolCallOutput{
					CallId: message.ToolCallId,
					Output: messageText(message),
					Type:   responses.ItemType_function_call_output,
				},
			},
		}}
	}
	item := convertEasyMessage(message)
	if item == nil {
		return nil
	}
	return []*responses.InputItem{item}
}

func convertEasyMessage(message *modelhubv1.Message) *responses.InputItem {
	var contentItems []*responses.ContentItem
	for _, part := range message.Parts {
		switch value := part.Content.(type) {
		case *modelhubv1.ContentPart_Text:
			if value.Text == "" {
				continue
			}
			contentItems = append(contentItems, &responses.ContentItem{
				Union: &responses.ContentItem_Text{
					Text: &responses.ContentItemText{
						Type: responses.ContentItemType_input_text,
						Text: value.Text,
					},
				},
			})
		case *modelhubv1.ContentPart_Image:
			if url := mediaURL(value.Image); url != "" {
				contentItems = append(contentItems, &responses.ContentItem{
					Union: &responses.ContentItem_Image{
						Image: &responses.ContentItemImage{
							Type:     responses.ContentItemType_input_image,
							ImageUrl: ptr(url),
						},
					},
				})
			}
		case *modelhubv1.ContentPart_Video:
			if url := mediaURL(value.Video); url != "" {
				contentItems = append(contentItems, &responses.ContentItem{
					Union: &responses.ContentItem_Video{
						Video: &responses.ContentItemVideo{
							Type:     responses.ContentItemType_input_video,
							VideoUrl: url,
						},
					},
				})
			}
		}
	}
	if len(contentItems) == 0 {
		return nil
	}
	if len(contentItems) == 1 {
		if textItem := contentItems[0].GetText(); textItem != nil {
			return &responses.InputItem{
				Union: &responses.InputItem_EasyMessage{
					EasyMessage: &responses.ItemEasyMessage{
						Role: convertRole(message.Role),
						Content: &responses.MessageContent{
							Union: &responses.MessageContent_StringValue{StringValue: textItem.Text},
						},
					},
				},
			}
		}
	}
	return &responses.InputItem{
		Union: &responses.InputItem_EasyMessage{
			EasyMessage: &responses.ItemEasyMessage{
				Role: convertRole(message.Role),
				Content: &responses.MessageContent{
					Union: &responses.MessageContent_ListValue{
						ListValue: &responses.ContentItemList{ListValue: contentItems},
					},
				},
			},
		},
	}
}

func convertRole(role modelhubv1.Role) responses.MessageRole_Enum {
	switch role {
	case modelhubv1.Role_ROLE_SYSTEM:
		return roleSystem
	case modelhubv1.Role_ROLE_ASSISTANT:
		return roleAssistant
	default:
		return roleUser
	}
}

func (p *Provider) convertResponse(resp *responses.ResponseObject) *modelhubv1.GenerateTextResponse {
	if resp == nil {
		return &modelhubv1.GenerateTextResponse{}
	}
	result := &modelhubv1.GenerateTextResponse{
		ResponseId:   resp.Id,
		FinishReason: resp.Status.String(),
		Usage:        p.extractUsage(resp),
	}
	var content strings.Builder
	for _, item := range resp.Output {
		if msg := item.GetOutputMessage(); msg != nil {
			for _, part := range msg.Content {
				if text := part.GetText(); text != nil {
					content.WriteString(text.Text)
				}
			}
		}
		if fc := item.GetFunctionToolCall(); fc != nil {
			result.ToolCalls = append(result.ToolCalls, &modelhubv1.ToolCall{
				Id:            fc.CallId,
				Name:          fc.Name,
				ArgumentsJson: []byte(fc.Arguments),
			})
		}
	}
	result.Content = content.String()
	return result
}

func (p *Provider) extractUsage(resp *responses.ResponseObject) *modelhubv1.Usage {
	if resp == nil || resp.Usage == nil {
		return nil
	}
	usage := &modelhubv1.Usage{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}
	if resp.Usage.InputTokensDetails != nil {
		usage.CachedTokens = resp.Usage.InputTokensDetails.CachedTokens
	}
	if resp.Usage.OutputTokensDetails != nil {
		usage.ReasoningTokens = resp.Usage.OutputTokensDetails.ReasoningTokens
	}
	return usage
}

func (p *Provider) buildTools(tools []*modelhubv1.Tool) ([]*responses.ResponsesTool, error) {
	arkTools := make([]*responses.ResponsesTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || tool.Function == nil || tool.Function.Name == "" {
			continue
		}
		description := tool.Function.Description
		arkTools = append(arkTools, &responses.ResponsesTool{
			Union: &responses.ResponsesTool_ToolFunction{
				ToolFunction: &responses.ToolFunction{
					Name:        tool.Function.Name,
					Type:        responses.ToolType_function,
					Description: ptr(description),
					Parameters:  &responses.Bytes{Value: tool.Function.ParametersJsonSchema},
				},
			},
		})
	}
	if len(arkTools) == 0 && len(tools) > 0 {
		return nil, provider.New(provider.ErrorInvalidArgument, p.name+" tools are invalid")
	}
	return arkTools, nil
}

func hasToolResultMessage(messages []*modelhubv1.Message) bool {
	for _, message := range messages {
		if message.Role == modelhubv1.Role_ROLE_TOOL {
			return true
		}
	}
	return false
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

func (p *Provider) mapError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiError *arkmodel.APIError
	if errors.As(err, &apiError) && apiError != nil && apiError.HTTPStatusCode != 0 {
		return provider.FromHTTP(p.name, apiError.HTTPStatusCode)
	}
	var requestError *arkmodel.RequestError
	if errors.As(err, &requestError) && requestError != nil && requestError.HTTPStatusCode != 0 {
		return provider.FromHTTP(p.name, requestError.HTTPStatusCode)
	}
	return provider.Wrap(provider.ErrorUnavailable, p.name+" "+operation+" failed", err)
}

func ptr[T any](value T) *T {
	return &value
}

var _ provider.TextProvider = (*Provider)(nil)
