package models

// Category 是模型自己的产品用途，不是某个调用方这次拿它干什么。
// 能看图并回文本的归 multimodal。纯文本对话归 llm。两类清单互不包含。
// *-image 出图 ID 仍归 image_generation。
type Category string

const (
	CategoryLLM             Category = "llm"
	CategoryMultimodal      Category = "multimodal"
	CategoryImageGeneration Category = "image_generation"
	CategoryVideoGeneration Category = "video_generation"
	CategorySpeech          Category = "speech"
	CategoryEmbedding       Category = "embedding"
	CategoryRerank          Category = "rerank"
	CategoryFaceCompare     Category = "face_compare"
	CategoryFaceDetect      Category = "face_detect"
	CategoryFaceLibrary     Category = "face_library"
	// CategoryPersonDetect 是画面里的人框。和检脸、看图说话分开。
	CategoryPersonDetect Category = "person_detect"
	// CategoryHumanParser 是人体和衣服区域解析，输出分割而不是框。
	CategoryHumanParser Category = "human_parser"
	// CategorySegment 是抠图。透明图出，不是文生图。
	CategorySegment Category = "segment"
)

var categories = map[string]Category{
	// 纯文本对话。DeepSeek 视觉是另一个未接入的 ID；qwen-flash 不收图。
	DeepSeekV4Flash:  CategoryLLM,
	DeepSeekV41Flash: CategoryLLM,
	QwenFlash:        CategoryLLM,

	Gemini25Flash:     CategoryMultimodal,
	Gemini37Flash:     CategoryMultimodal,
	Gemini38Flash:     CategoryMultimodal,
	Gemini35FlashLite: CategoryMultimodal,
	Gemini20Flash001:  CategoryMultimodal,
	DoubaoSeed16:      CategoryMultimodal,
	DoubaoSeed20Mini:  CategoryMultimodal,
	DoubaoSeed20Lite:  CategoryMultimodal,
	DoubaoSeed21Pro:   CategoryMultimodal,
	Qwen3VLPlus:       CategoryMultimodal,
	Qwen35Flash:       CategoryMultimodal,
	Qwen37Flash:       CategoryMultimodal,
	Qwen38Flash:       CategoryMultimodal,
	ClaudeHaiku45:     CategoryMultimodal,
	GLM53Flash:        CategoryMultimodal,

	Qwen3VLEmbedding:     CategoryEmbedding,
	CohereEmbedV4:        CategoryEmbedding,
	CohereEmbedV4Bedrock: CategoryEmbedding,

	Qwen37TextRerank: CategoryRerank,
	Qwen3VLRerank:    CategoryRerank,
	CohereRerankV35:         CategoryRerank,
	CohereRerankV35Official: CategoryRerank,

	FacebodyCompareFace:     CategoryFaceCompare,
	RekognitionCompareFaces: CategoryFaceCompare,

	FacebodyDetectFace:     CategoryFaceDetect,
	RekognitionDetectFaces: CategoryFaceDetect,

	FacebodyFaceLibrary:    CategoryFaceLibrary,
	RekognitionFaceLibrary: CategoryFaceLibrary,

	Gemini3ProImage:         CategoryImageGeneration,
	Gemini25FlashImage:      CategoryImageGeneration,
	Gemini31FlashImage:      CategoryImageGeneration,
	GPTImage2:               CategoryImageGeneration,
	GPTImage25Flare:         CategoryImageGeneration,
	GPTImage25Sunburst:      CategoryImageGeneration,
	Flux2Klein9B:            CategoryImageGeneration,
	PhotoroomSegment:        CategorySegment,
	SegmentPersonBria:       CategorySegment,
	SegmentSubjectBria:      CategorySegment,
	HumanYOLO:               CategoryPersonDetect,
	RekognitionDetectLabels: CategoryPersonDetect,
	HumanParser:             CategoryHumanParser,

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
	case CategoryLLM, CategoryMultimodal, CategoryImageGeneration, CategoryVideoGeneration, CategorySpeech,
		CategoryEmbedding, CategoryRerank, CategoryFaceCompare, CategoryFaceDetect, CategoryFaceLibrary,
		CategoryPersonDetect, CategoryHumanParser, CategorySegment:
		return true
	default:
		return false
	}
}
