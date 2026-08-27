package models

import "testing"

// TestGemini35FlashInCatalog 锁定 gemini-3.5-flash 已进入 models 目录，供衣橱深度搭配等调用方引用。
func TestGemini35FlashInCatalog(t *testing.T) {
	for _, model := range All() {
		if model == Gemini35Flash {
			return
		}
	}
	t.Fatalf("Gemini35Flash=%q missing from All()", Gemini35Flash)
}

func TestAllAreUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for _, model := range All() {
		if model == "" {
			t.Fatal("model id is empty")
		}
		if _, ok := seen[model]; ok {
			t.Fatalf("duplicate model id %s", model)
		}
		seen[model] = struct{}{}
	}
}
