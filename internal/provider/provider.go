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

// VideoProvider 只暴露 Submit/Get/ReadResult；异步任务账本与对外 RPC 经 Service 编排，禁止同步 GenerateVideo 包装。
type VideoProvider interface {
	SubmitVideo(context.Context, string, *modelhubv2.GenerateRequest) (providerTaskID string, err error)
	GetVideo(context.Context, string, string) (VideoJob, error)
	ReadVideoResult(context.Context, string, string, EmitEvent) error
}

// Set 表示一个已配置供应商真正实现的能力，不用空实现伪装未支持的 RPC。
type Set struct {
	Text   TextProvider
	Image  ImageProvider
	Video  VideoProvider
	Speech SpeechProvider
	ASR    ASRProvider
}

// SpeechProvider 承接同步一次性 TTS；成功必须返回完整音频，半截收集只能以 error 结束。
type SpeechProvider interface {
	SynthesizeSpeech(context.Context, string, *modelhubv2.SynthesizeSpeechRequest) (*modelhubv2.SynthesizeSpeechResponse, error)
}

// StreamingSpeechProvider 只由支持真实增量音频的供应商实现，不回退到整段合成再分包。
// emit 返回错误后必须停止读取；音频切片仅在回调期间有效。
// SpeechMIME 标明这些切片的编码。Hub 靠它决定要不要再解码。
type StreamingSpeechProvider interface {
	SynthesizeSpeechStream(context.Context, string, *modelhubv2.SynthesizeSpeechRequest, func([]byte) error) error
	SpeechMIME() string
}

// ASREmit 由供应商读循环调用；回调返回错误表示客户端流已不可写，供应商必须尽快停止。
type ASREmit func(*modelhubv2.TranscribeSpeechTranscript) error

// ASRProvider 为每条 gRPC 流创建独立会话。供应商连接不能跨调用共享，否则 stop/取消会串线。
type ASRProvider interface {
	OpenASR(context.Context, string, *modelhubv2.TranscribeSpeechStart, ASREmit) (ASRSession, error)
}

// ASRSession 只承载已经建立的单条双向会话；Finalize 不关闭连接，Stop 才释放计费资源。
type ASRSession interface {
	ID() string
	SendAudio([]byte) error
	Finalize() error
	Stop() error
	Errors() <-chan error
}
