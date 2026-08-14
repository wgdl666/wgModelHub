package provider

import (
	"context"

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
)

type EmitTextEvent func(*modelhubv1.TextStreamEvent) error
type EmitVideoChunk func(*modelhubv1.VideoChunk) error

// TextProvider 只接收已经由 profile 解析出的模型名；供应商地址与凭据不会进入 RPC。
type TextProvider interface {
	Generate(context.Context, string, *modelhubv1.GenerateTextRequest) (*modelhubv1.GenerateTextResponse, error)
	GenerateStream(context.Context, string, *modelhubv1.GenerateTextRequest, EmitTextEvent) (*modelhubv1.GenerateTextResponse, error)
}

type ImageProvider interface {
	GenerateImage(context.Context, string, *modelhubv1.GenerateImageRequest) (*modelhubv1.GenerateImageResponse, error)
}

type VideoProvider interface {
	GenerateVideo(context.Context, string, *modelhubv1.GenerateVideoRequest, EmitVideoChunk) error
}

// Set 表示一个已配置供应商真正实现的能力，不用空实现伪装未支持的 RPC。
type Set struct {
	Text  TextProvider
	Image ImageProvider
	Video VideoProvider
}
