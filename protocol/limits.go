package protocol

const (
	// MaxMediaBytes 约束单个内联媒体，避免图片 RPC 无界占用内存。
	MaxMediaBytes = 64 << 20
	// MaxRPCMessageBytes 是 ModelHub 服务端及所有调用方统一使用的 gRPC 单消息上限。
	MaxRPCMessageBytes = 64 << 20
	// VideoChunkBytes 固定 LTX 视频分块大小，调用方据此校验流协议而不接收任意大消息。
	VideoChunkBytes = 1 << 20
	// MaxVideoBytes 是一次同步 LTX 调用允许返回的完整视频上限。
	MaxVideoBytes = 200 << 20
)
