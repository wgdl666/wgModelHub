package profile

// 业务 profile 名称必须稳定且只在此集中定义。
// 调用方只持有这些常量，不能再携带供应商模型名、地址或密钥。
const (
	HubChat          = "hub.chat"
	HubQuickSpeech   = "hub.quick_speech"
	HubBrainstorm    = "hub.brainstorm"
	AsyncVision      = "async.vision"
	AsyncCandidate   = "async.candidate"
	AsyncArtwork     = "async.artwork"
	AsyncClothImage  = "async.cloth_image"
	AsyncBodyModel   = "async.body_model"
	AsyncFaceStylize = "async.face_stylize"
	AsyncVTONPrimary = "async.vton.primary"
	AsyncVTONBackup  = "async.vton.backup"
	AsyncLTXVideo    = "async.ltx.video"
)

// Required 返回首版启动前必须在 Nacos 中配齐的全部 profile。
// 缺任一 profile 时服务拒绝启动，避免调用方在运行时才发现映射缺失。
func Required() []string {
	return []string{
		HubChat,
		HubQuickSpeech,
		HubBrainstorm,
		AsyncVision,
		AsyncCandidate,
		AsyncArtwork,
		AsyncClothImage,
		AsyncBodyModel,
		AsyncFaceStylize,
		AsyncVTONPrimary,
		AsyncVTONBackup,
		AsyncLTXVideo,
	}
}
