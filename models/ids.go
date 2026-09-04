// Package models 是 ModelHub 承认的真实供应商模型 ID 清单。
// 调用方应引用这些常量作为 request.model，禁止再写业务 alias。
package models

const (
	Gemini25Flash = "gemini-2.5-flash"
	// Gemini37Flash 是 Google GA 的 gemini-3.7-flash；不支持关闭 thinking，Hub DISABLED 须在 Gemini provider 映射为 LOW。
	Gemini37Flash    = "gemini-3.7-flash"
	Gemini20Flash001 = "gemini-2.0-flash-001"
	DoubaoSeed16     = "doubao-seed-1.6"
	// DoubaoSeed20Mini 对外真实模型名；上游 Responses API 需经各自 Ark endpoint_id 映射，不能暴露基础模型 ID，也不能与 Lite 互相 alias。
	DoubaoSeed20Mini = "doubao-seed-2.0-mini"
	// DoubaoSeed20Lite 对外真实模型名；与 Mini 分 endpoint 分 provider，禁止共用推理部署或互相替代。
	DoubaoSeed20Lite = "doubao-seed-2.0-lite"
	// DoubaoSeed21Pro 对外真实模型名；衣橱候选搭配等文本链路经独立 Ark endpoint 推理，与 2.0 mini/lite 分实例，禁止混用。
	DoubaoSeed21Pro = "doubao-seed-2.1-pro"
	// DeepSeekV4Flash 对外真实模型名（方舟正式版）；上游 Responses 走独立推理 endpoint，禁止与预览版或其他 DeepSeek ID 互相 alias。
	DeepSeekV4Flash    = "deepseek-v4-flash"
	QwenFlash          = "qwen-flash"
	Qwen3VLPlus        = "qwen3-vl-plus"
	Qwen35Flash        = "qwen3.5-flash"
	Qwen37Flash        = "qwen3.7-flash"
	Gemini3ProImage    = "gemini-3-pro-image"
	Gemini25FlashImage = "gemini-2.5-flash-image"
	Gemini31FlashImage = "gemini-3.1-flash-image"
	GPTImage2          = "gpt-image-2"
	LTX                = "ltx"

	// DashScope Wan / HappyHorse / Kling 图生视频。
	Wan22I2VFlash              = "wan2.2-i2v-flash"
	Wan22I2VPlus               = "wan2.2-i2v-plus"
	Wan22I2VTurbo              = "wan2.2-i2v-turbo"
	Wan26I2V                   = "wan2.6-i2v"
	Wan26I2VFlash              = "wan2.6-i2v-flash"
	Wan27I2V                   = "wan2.7-i2v"
	HappyHorse11I2V            = "happyhorse-1.1-i2v"
	HappyHorse10I2V            = "happyhorse-1.0-i2v"
	KlingV3VideoGeneration     = "kling/kling-v3-video-generation"
	KlingV3OmniVideoGeneration = "kling/kling-v3-omni-video-generation"
	Wan27VideoEdit             = "wan2.7-videoedit"
	// 火山方舟 Seedance 2.5 官方视频任务 ID。文生/首帧共用，与 Dreamina 2.0 别名不能互换。
	DoubaoSeedance25 = "doubao-seedance-2-5-260628"

	// OminiLink vg-api 视频生成（含 UI 未展示但 client 已识别的 Dreamina）。
	DreaminaSeedance20Mini = "dreamina-seedance-2-0-mini"
	DreaminaSeedance20     = "dreamina-seedance-2-0"
	DreaminaSeedance20Fast = "dreamina-seedance-2-0-fast"
	KlingV2Master          = "kling-v2-master"
	KlingV21Master         = "kling-v2-1-master"
	KlingV25Turbo          = "kling-v2-5-turbo"
	KlingV26               = "kling-v2-6"
	KlingVideoO1           = "kling-video-o1"
	KlingV3Omni            = "kling-v3-omni"
	KlingV3                = "kling-v3"
	ViduQ3                 = "viduq3"
	ViduQ3Mix              = "viduq3-mix"
	ViduQ3Turbo            = "viduq3-turbo"
	ViduQ3ProFast          = "viduq3-pro-fast"
	ViduQ3Pro              = "viduq3-pro"
	Veo31LiteGenerate001   = "veo-3.1-lite-generate-001"
	Veo31FastGenerate001   = "veo-3.1-fast-generate-001"
	Veo31Generate001       = "veo-3.1-generate-001"

	// Gemini Interactions 图生视频与编辑共用同一真实模型 ID。
	GeminiOmniFlashPreview = "gemini-omni-flash-preview"

	// Minimax 同步 TTS（与线上 wgHub DefaultMinimaxConfig 一致）。
	Speech28Turbo = "speech-2.8-turbo"
)

// All 返回当前仓库承认的全部真实模型 ID，顺序稳定便于对照。
func All() []string {
	return []string{
		Gemini25Flash,
		Gemini37Flash,
		Gemini20Flash001,
		DoubaoSeed16,
		DoubaoSeed20Mini,
		DoubaoSeed20Lite,
		DoubaoSeed21Pro,
		DeepSeekV4Flash,
		QwenFlash,
		Qwen3VLPlus,
		Qwen35Flash,
		Qwen37Flash,
		Gemini3ProImage,
		Gemini25FlashImage,
		Gemini31FlashImage,
		GPTImage2,
		LTX,
		Wan22I2VFlash,
		Wan22I2VPlus,
		Wan22I2VTurbo,
		Wan26I2V,
		Wan26I2VFlash,
		Wan27I2V,
		HappyHorse11I2V,
		HappyHorse10I2V,
		KlingV3VideoGeneration,
		KlingV3OmniVideoGeneration,
		Wan27VideoEdit,
		DoubaoSeedance25,
		DreaminaSeedance20Mini,
		DreaminaSeedance20,
		DreaminaSeedance20Fast,
		KlingV2Master,
		KlingV21Master,
		KlingV25Turbo,
		KlingV26,
		KlingVideoO1,
		KlingV3Omni,
		KlingV3,
		ViduQ3,
		ViduQ3Mix,
		ViduQ3Turbo,
		ViduQ3ProFast,
		ViduQ3Pro,
		Veo31LiteGenerate001,
		Veo31FastGenerate001,
		Veo31Generate001,
		GeminiOmniFlashPreview,
		Speech28Turbo,
	}
}
