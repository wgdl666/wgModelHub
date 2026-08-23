package provider

import (
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

// MessageParts 按 Input.items 顺序展开 Message.parts，供图片生成保留调用方多模态顺序。
func MessageParts(input *modelhubv2.Input) []*modelhubv2.ContentPart {
	if input == nil {
		return nil
	}
	var parts []*modelhubv2.ContentPart
	for _, item := range input.GetItems() {
		if message := item.GetMessage(); message != nil {
			parts = append(parts, message.GetParts()...)
		}
	}
	return parts
}

// FirstImageMedia 取 Input 中第一张图片，作为视频首帧或编辑参考图而不另开顶层字段。
func FirstImageMedia(input *modelhubv2.Input) *modelhubv2.Media {
	for _, part := range MessageParts(input) {
		if image := part.GetImage(); image != nil {
			return image
		}
	}
	return nil
}

// ImageMedias 按 Input.items 顺序收集全部图片，供视频编辑多参考图保持调用方顺序。
func ImageMedias(input *modelhubv2.Input) []*modelhubv2.Media {
	var images []*modelhubv2.Media
	for _, part := range MessageParts(input) {
		if image := part.GetImage(); image != nil {
			images = append(images, image)
		}
	}
	return images
}

// FirstVideoMedia 取 Input 中第一个视频 part，用于识别编辑请求形态。
func FirstVideoMedia(input *modelhubv2.Input) *modelhubv2.Media {
	for _, part := range MessageParts(input) {
		if video := part.GetVideo(); video != nil {
			return video
		}
	}
	return nil
}

// MediaURI 从 Media 取出可下发供应商的 URI；内联媒体不在此函数处理。
func MediaURI(media *modelhubv2.Media) string {
	if media == nil {
		return ""
	}
	if uri, ok := media.Source.(*modelhubv2.Media_Uri); ok {
		return strings.TrimSpace(uri.Uri)
	}
	return ""
}

// JoinedText 按 Input.items 顺序拼接 Message 文本 parts；视频等能力用它取任务文案，禁止另设顶层 prompt。
func JoinedText(input *modelhubv2.Input) string {
	var texts []string
	for _, part := range MessageParts(input) {
		if text := part.GetText(); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

// TextFinalEvent 组装非流式文本终态事件，完整正文与工具调用一并返回。
func TextFinalEvent(content string, toolCalls []*modelhubv2.ToolCall, responseID, finishReason string, usage *modelhubv2.Usage) *modelhubv2.GenerateEvent {
	event := &modelhubv2.GenerateEvent{
		Final:        true,
		ResponseId:   responseID,
		FinishReason: finishReason,
		Usage:        usage,
	}
	if content != "" {
		event.Items = append(event.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Text{Text: content}})
	}
	for _, call := range toolCalls {
		event.Items = append(event.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: call}})
	}
	return event
}

// TextDeltaEvent 构造非 final 文本增量。
func TextDeltaEvent(delta string) *modelhubv2.GenerateEvent {
	return &modelhubv2.GenerateEvent{
		Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: delta}}},
	}
}

// ToolCallEvent 构造非 final 工具调用增量。
func ToolCallEvent(call *modelhubv2.ToolCall) *modelhubv2.GenerateEvent {
	return &modelhubv2.GenerateEvent{
		Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: call}}},
	}
}

// MetadataFinalEvent 供文本 stream 返回给 service 的终态元数据（不含累计正文）。
func MetadataFinalEvent(responseID, finishReason string, usage *modelhubv2.Usage) *modelhubv2.GenerateEvent {
	return &modelhubv2.GenerateEvent{
		Final:        true,
		ResponseId:   responseID,
		FinishReason: finishReason,
		Usage:        usage,
	}
}
