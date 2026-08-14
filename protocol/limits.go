package protocol

const (
	// MaxRPCMessageBytes 是 ModelHub 服务端及所有调用方统一使用的 gRPC 单消息上限。
	MaxRPCMessageBytes = 64 << 20
	// MaxMediaBytes 约束单个内联媒体。刻意低于 MaxRPCMessageBytes，为 protobuf/gRPC
	// 消息头、字段标签与其它标量预留包装空间；若与 RPC 上限相等，接近满载的媒体
	// 在序列化后会超过 64MiB 而无法发送。
	MaxMediaBytes = 60 << 20
	// VideoChunkBytes 固定 LTX 视频分块大小，调用方据此校验流协议而不接收任意大消息。
	VideoChunkBytes = 1 << 20
	// MaxVideoBytes 是一次同步 LTX 调用允许返回的完整视频上限。
	MaxVideoBytes = 200 << 20
)
