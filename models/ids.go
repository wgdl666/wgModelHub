// Package models 是 ModelHub 承认的真实供应商模型 ID 清单。
// 调用方应引用这些常量作为 request.model，禁止再写业务 alias。
package models

const (
	Gemini25Flash      = "gemini-2.5-flash"
	Gemini20Flash001   = "gemini-2.0-flash-001"
	DoubaoSeed16       = "doubao-seed-1.6"
	QwenFlash          = "qwen-flash"
	Qwen3VLPlus        = "qwen3-vl-plus"
	Qwen35Flash        = "qwen3.5-flash"
	Qwen37Flash        = "qwen3.7-flash"
	Gemini3ProImage    = "gemini-3-pro-image"
	Gemini25FlashImage = "gemini-2.5-flash-image"
	Gemini31FlashImage = "gemini-3.1-flash-image"
	GPTImage2          = "gpt-image-2"
	LTX                = "ltx"
)

// All 返回当前仓库承认的全部真实模型 ID，顺序稳定便于对照。
func All() []string {
	return []string{
		Gemini25Flash,
		Gemini20Flash001,
		DoubaoSeed16,
		QwenFlash,
		Qwen3VLPlus,
		Qwen35Flash,
		Qwen37Flash,
		Gemini3ProImage,
		Gemini25FlashImage,
		Gemini31FlashImage,
		GPTImage2,
		LTX,
	}
}
