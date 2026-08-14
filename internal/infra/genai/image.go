package genai

import (
	"context"

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	"github.com/wgdl666/wgModelHub/internal/provider"
	genaisdk "google.golang.org/genai"
)

// GenerateImage 走 Gemini 多模态生图；输入 parts 顺序与 RPC 一致，响应也按供应商返回顺序展开。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv1.GenerateImageRequest) (*modelhubv1.GenerateImageResponse, error) {
	parts := make([]*genaisdk.Part, 0, len(request.Parts))
	for _, part := range request.Parts {
		if converted := convertContentPart(part); converted != nil {
			parts = append(parts, converted)
		}
	}
	contents := []*genaisdk.Content{genaisdk.NewContentFromParts(parts, genaisdk.RoleUser)}
	response, err := p.client.Models.GenerateContent(ctx, model, contents, buildImageConfig(request))
	if err != nil {
		return nil, p.mapError(ctx, "generate image", err)
	}
	return convertImageResponse(response), nil
}

func buildImageConfig(request *modelhubv1.GenerateImageRequest) *genaisdk.GenerateContentConfig {
	cfg := &genaisdk.GenerateContentConfig{}
	if len(request.OutputModalities) > 0 {
		for _, modality := range request.OutputModalities {
			switch modality {
			case modelhubv1.ImageOutputModality_IMAGE_OUTPUT_MODALITY_IMAGE:
				cfg.ResponseModalities = append(cfg.ResponseModalities, "IMAGE")
			case modelhubv1.ImageOutputModality_IMAGE_OUTPUT_MODALITY_TEXT:
				cfg.ResponseModalities = append(cfg.ResponseModalities, "TEXT")
			}
		}
	}
	if request.AspectRatio != nil || request.ImageSize != nil {
		cfg.ImageConfig = &genaisdk.ImageConfig{}
		if request.AspectRatio != nil {
			cfg.ImageConfig.AspectRatio = *request.AspectRatio
		}
		if request.ImageSize != nil {
			cfg.ImageConfig.ImageSize = *request.ImageSize
		}
	}
	// optional 已设置时必须原样下发，否则显式 temperature=0 会被误判为“未配置”。
	if request.Temperature != nil {
		value := float32(*request.Temperature)
		cfg.Temperature = &value
	}
	if request.ThinkingLevel != modelhubv1.ThinkingLevel_THINKING_LEVEL_UNSPECIFIED {
		cfg.ThinkingConfig = &genaisdk.ThinkingConfig{
			ThinkingLevel: convertImageThinkingLevel(request.ThinkingLevel),
		}
	}
	return cfg
}

func convertImageThinkingLevel(level modelhubv1.ThinkingLevel) genaisdk.ThinkingLevel {
	switch level {
	case modelhubv1.ThinkingLevel_THINKING_LEVEL_MINIMAL:
		return genaisdk.ThinkingLevelMinimal
	case modelhubv1.ThinkingLevel_THINKING_LEVEL_LOW:
		return genaisdk.ThinkingLevelLow
	case modelhubv1.ThinkingLevel_THINKING_LEVEL_MEDIUM:
		return genaisdk.ThinkingLevelMedium
	case modelhubv1.ThinkingLevel_THINKING_LEVEL_HIGH:
		return genaisdk.ThinkingLevelHigh
	default:
		return genaisdk.ThinkingLevelUnspecified
	}
}

func convertImageResponse(response *genaisdk.GenerateContentResponse) *modelhubv1.GenerateImageResponse {
	if response == nil {
		return &modelhubv1.GenerateImageResponse{}
	}
	result := &modelhubv1.GenerateImageResponse{Usage: convertUsage(response.UsageMetadata)}
	if feedback := response.PromptFeedback; feedback != nil {
		safety := &modelhubv1.SafetyFeedback{
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
				result.Parts = append(result.Parts, &modelhubv1.ContentPart{
					Content: &modelhubv1.ContentPart_Text{Text: part.Text},
				})
			}
			if part.InlineData != nil && len(part.InlineData.Data) > 0 {
				result.Parts = append(result.Parts, &modelhubv1.ContentPart{
					Content: &modelhubv1.ContentPart_Image{
						Image: &modelhubv1.Media{
							MimeType: part.InlineData.MIMEType,
							Source:   &modelhubv1.Media_Data{Data: append([]byte(nil), part.InlineData.Data...)},
						},
					},
				})
			}
		}
		if candidate.FinishReason != "" {
			result.FinishReason = string(candidate.FinishReason)
		}
	}
	if result.Safety != nil && result.Safety.Blocked {
		return result
	}
	return result
}

// buildImageParts 仅供测试断言输入顺序；生产路径在 GenerateImage 内联展开以保持与 RPC 一致。
func buildImageParts(request *modelhubv1.GenerateImageRequest) []*genaisdk.Part {
	parts := make([]*genaisdk.Part, 0, len(request.Parts))
	for _, part := range request.Parts {
		if converted := convertContentPart(part); converted != nil {
			parts = append(parts, converted)
		}
	}
	return parts
}

// Ensure Provider implements image capability at compile time.
var _ provider.ImageProvider = (*Provider)(nil)
