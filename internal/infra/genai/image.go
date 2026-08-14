package genai

import (
	"context"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	genaisdk "google.golang.org/genai"
)

// GenerateImage 走 Gemini 多模态生图；Input 中 Message.parts 顺序与 RPC 一致，响应也按供应商返回顺序展开。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	parts := buildImageParts(request)
	contents := []*genaisdk.Content{genaisdk.NewContentFromParts(parts, genaisdk.RoleUser)}
	response, err := p.client.Models.GenerateContent(ctx, model, contents, buildImageConfig(request))
	if err != nil {
		return nil, p.mapError(ctx, "generate image", err)
	}
	return convertImageResponse(response), nil
}

func buildImageConfig(request *modelhubv2.GenerateRequest) *genaisdk.GenerateContentConfig {
	image := request.GetOutput().GetImage()
	cfg := &genaisdk.GenerateContentConfig{}
	if image == nil {
		return cfg
	}
	if len(image.OutputModalities) > 0 {
		for _, modality := range image.OutputModalities {
			switch modality {
			case modelhubv2.ImageOutputModality_IMAGE_OUTPUT_MODALITY_IMAGE:
				cfg.ResponseModalities = append(cfg.ResponseModalities, "IMAGE")
			case modelhubv2.ImageOutputModality_IMAGE_OUTPUT_MODALITY_TEXT:
				cfg.ResponseModalities = append(cfg.ResponseModalities, "TEXT")
			}
		}
	}
	if image.AspectRatio != nil || image.ImageSize != nil {
		cfg.ImageConfig = &genaisdk.ImageConfig{}
		if image.AspectRatio != nil {
			cfg.ImageConfig.AspectRatio = *image.AspectRatio
		}
		if image.ImageSize != nil {
			cfg.ImageConfig.ImageSize = *image.ImageSize
		}
	}
	// optional 已设置时必须原样下发，否则显式 temperature=0 会被误判为“未配置”。
	if image.Temperature != nil {
		value := float32(*image.Temperature)
		cfg.Temperature = &value
	}
	if image.ThinkingLevel != modelhubv2.ThinkingLevel_THINKING_LEVEL_UNSPECIFIED {
		cfg.ThinkingConfig = &genaisdk.ThinkingConfig{
			ThinkingLevel: convertImageThinkingLevel(image.ThinkingLevel),
		}
	}
	return cfg
}

func convertImageThinkingLevel(level modelhubv2.ThinkingLevel) genaisdk.ThinkingLevel {
	switch level {
	case modelhubv2.ThinkingLevel_THINKING_LEVEL_MINIMAL:
		return genaisdk.ThinkingLevelMinimal
	case modelhubv2.ThinkingLevel_THINKING_LEVEL_LOW:
		return genaisdk.ThinkingLevelLow
	case modelhubv2.ThinkingLevel_THINKING_LEVEL_MEDIUM:
		return genaisdk.ThinkingLevelMedium
	case modelhubv2.ThinkingLevel_THINKING_LEVEL_HIGH:
		return genaisdk.ThinkingLevelHigh
	default:
		return genaisdk.ThinkingLevelUnspecified
	}
}

func convertImageResponse(response *genaisdk.GenerateContentResponse) *modelhubv2.GenerateEvent {
	if response == nil {
		return &modelhubv2.GenerateEvent{Final: true}
	}
	result := &modelhubv2.GenerateEvent{Final: true, Usage: convertUsage(response.UsageMetadata)}
	if feedback := response.PromptFeedback; feedback != nil {
		safety := &modelhubv2.SafetyFeedback{
			Reason:  string(feedback.BlockReason),
			Message: feedback.BlockReasonMessage,
		}
		if feedback.BlockReason != "" && feedback.BlockReason != genaisdk.BlockedReasonUnspecified {
			safety.Blocked = true
		}
		for _, rating := range feedback.SafetyRatings {
			if rating != nil && rating.Blocked {
				safety.Blocked = true
			}
		}
		result.Safety = safety
	}
	for _, candidate := range response.Candidates {
		if candidate.Content == nil {
			continue
		}
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				result.Items = append(result.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Text{Text: part.Text}})
			}
			if part.InlineData != nil && len(part.InlineData.Data) > 0 {
				result.Items = append(result.Items, &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
					MimeType: part.InlineData.MIMEType,
					Source:   &modelhubv2.Media_Data{Data: append([]byte(nil), part.InlineData.Data...)},
				}}})
			}
		}
		if candidate.FinishReason != "" {
			result.FinishReason = string(candidate.FinishReason)
		}
	}
	return result
}

// buildImageParts 按 Input.items 中 Message.parts 原序展开；任务文案与参考图同属有序上下文。
func buildImageParts(request *modelhubv2.GenerateRequest) []*genaisdk.Part {
	contentParts := provider.MessageParts(request.GetInput())
	parts := make([]*genaisdk.Part, 0, len(contentParts))
	for _, part := range contentParts {
		if converted := convertContentPart(part); converted != nil {
			parts = append(parts, converted)
		}
	}
	return parts
}

// Ensure Provider implements image capability at compile time.
var _ provider.ImageProvider = (*Provider)(nil)
