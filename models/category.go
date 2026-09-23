package models

// Category 是产品主用途，不是 OutputSpec capability。
// Gemini 聊天模型即使能收图仍归 llm，避免和 *-image 出图 ID 混成一类。
type Category string

const (
	CategoryLLM             Category = "llm"
	CategoryMultimodal      Category = "multimodal"
	CategoryImageGeneration Category = "image_generation"
	CategoryVideoGeneration Category = "video_generation"
	CategorySpeech          Category = "speech"
)

var categories = map[string]Category{
	Gemini25Flash:     CategoryLLM,
	Gemini37Flash:     CategoryLLM,
	Gemini38Flash:     CategoryLLM,
	Gemini35FlashLite: CategoryLLM,
	Gemini20Flash001:  CategoryLLM,
	DoubaoSeed16:      CategoryLLM,
	DoubaoSeed20Mini:  CategoryLLM,
	DoubaoSeed20Lite:  CategoryLLM,
	DoubaoSeed21Pro:   CategoryLLM,
	DeepSeekV4Flash:   CategoryLLM,
	DeepSeekV41Flash:  CategoryLLM,
	QwenFlash:         CategoryLLM,
	Qwen35Flash:       CategoryLLM,
	Qwen37Flash:       CategoryLLM,
	Qwen38Flash:       CategoryLLM,
	ClaudeHaiku45:     CategoryLLM,
	GLM53Flash:        CategoryLLM,

	Qwen3VLPlus: CategoryMultimodal,

	Gemini3ProImage:         CategoryImageGeneration,
	Gemini25FlashImage:      CategoryImageGeneration,
	Gemini31FlashImage:      CategoryImageGeneration,
	GPTImage2:               CategoryImageGeneration,
	GPTImage25Flare:         CategoryImageGeneration,
	GPTImage25Sunburst:      CategoryImageGeneration,
	Flux2Klein9B:            CategoryImageGeneration,
	PhotoroomSegment:        CategoryImageGeneration,
	SegmentPersonBria:       CategoryImageGeneration,
	SegmentSubjectBria:      CategoryImageGeneration,
	HumanYOLO:               CategoryMultimodal,
	RekognitionDetectLabels: CategoryMultimodal,
	FacebodyCompareFace:     CategoryMultimodal,
	RekognitionCompareFaces: CategoryMultimodal,
	HumanParser:             CategoryMultimodal,

	LTX:                        CategoryVideoGeneration,
	Wan22I2VFlash:              CategoryVideoGeneration,
	Wan22I2VPlus:               CategoryVideoGeneration,
	Wan22I2VTurbo:              CategoryVideoGeneration,
	Wan26I2V:                   CategoryVideoGeneration,
	Wan26I2VFlash:              CategoryVideoGeneration,
	Wan27I2V:                   CategoryVideoGeneration,
	HappyHorse11I2V:            CategoryVideoGeneration,
	HappyHorse10I2V:            CategoryVideoGeneration,
	KlingV3VideoGeneration:     CategoryVideoGeneration,
	KlingV3OmniVideoGeneration: CategoryVideoGeneration,
	Wan27VideoEdit:             CategoryVideoGeneration,
	DoubaoSeedance25:           CategoryVideoGeneration,
	DreaminaSeedance20Mini:     CategoryVideoGeneration,
	DreaminaSeedance20:         CategoryVideoGeneration,
	DreaminaSeedance20Fast:     CategoryVideoGeneration,
	KlingV2Master:              CategoryVideoGeneration,
	KlingV21Master:             CategoryVideoGeneration,
	KlingV25Turbo:              CategoryVideoGeneration,
	KlingV26:                   CategoryVideoGeneration,
	KlingVideoO1:               CategoryVideoGeneration,
	KlingV3Omni:                CategoryVideoGeneration,
	KlingV3:                    CategoryVideoGeneration,
	ViduQ3:                     CategoryVideoGeneration,
	ViduQ3Mix:                  CategoryVideoGeneration,
	ViduQ3Turbo:                CategoryVideoGeneration,
	ViduQ3ProFast:              CategoryVideoGeneration,
	ViduQ3Pro:                  CategoryVideoGeneration,
	Veo31LiteGenerate001:       CategoryVideoGeneration,
	Veo31FastGenerate001:       CategoryVideoGeneration,
	Veo31Generate001:           CategoryVideoGeneration,
	GeminiOmniFlashPreview:     CategoryVideoGeneration,

	Speech28Turbo:  CategorySpeech,
	ElevenFlashV25: CategorySpeech,
}

// CategoryOf 返回真实模型 ID 的产品分类；未知 ID 不得猜测。
func CategoryOf(id string) (Category, bool) {
	category, ok := categories[id]
	return category, ok
}

// Public 表示该分类会出现在 ListModels 对外清单里。
func (c Category) Public() bool {
	switch c {
	case CategoryLLM, CategoryMultimodal, CategoryImageGeneration, CategoryVideoGeneration, CategorySpeech:
		return true
	default:
		return false
	}
}
