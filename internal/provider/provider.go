package provider

import (
	"context"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

// EmitEvent 推送文本增量、工具事件或视频分块；文本 stream 的唯一 final 由 service 统一发送。
type EmitEvent func(*modelhubv2.GenerateEvent) error

// TextProvider 只接收调用方传入的真实供应商模型 ID；供应商地址与凭据不会进入 RPC。
type TextProvider interface {
	Generate(context.Context, string, *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error)
	GenerateStream(context.Context, string, *modelhubv2.GenerateRequest, EmitEvent) (*modelhubv2.GenerateEvent, error)
}

// CachedContentCreator 由支持显式前缀缓存的 TextProvider（如 Gemini）实现。
type CachedContentCreator interface {
	CreateCachedContent(context.Context, string, *modelhubv2.CreateCachedContentRequest) (*modelhubv2.CreateCachedContentResponse, error)
}

type ImageProvider interface {
	GenerateImage(context.Context, string, *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error)
}

type VideoProvider interface {
	GenerateVideo(context.Context, string, *modelhubv2.GenerateRequest, EmitEvent) error
}

// Set 表示一个已配置供应商真正实现的能力，不用空实现伪装未支持的 RPC。
type Set struct {
	Text  TextProvider
	Image ImageProvider
	Video VideoProvider
}
