package models

import "testing"

func TestCategoryOfCoversAll(t *testing.T) {
	for _, id := range All() {
		if _, ok := CategoryOf(id); !ok {
			t.Fatalf("CategoryOf missing %q; new IDs must be classified", id)
		}
	}
	if len(categories) != len(All()) {
		t.Fatalf("categories=%d All=%d; every catalog ID must have exactly one category", len(categories), len(All()))
	}
}

func TestCategoryOfKnownIDs(t *testing.T) {
	cases := []struct {
		id   string
		want Category
	}{
		{Gemini37Flash, CategoryLLM},
		{DeepSeekV4Flash, CategoryLLM},
		{DeepSeekV41Flash, CategoryLLM},
		{GLM53Flash, CategoryLLM},
		{Qwen3VLPlus, CategoryMultimodal},
		{GPTImage2, CategoryImageGeneration},
		{GPTImage25Flare, CategoryImageGeneration},
		{GPTImage25Sunburst, CategoryImageGeneration},
		{Flux2Klein9B, CategoryImageGeneration},
		{PhotoroomSegment, CategoryImageGeneration},
		{LTX, CategoryVideoGeneration},
		{GeminiOmniFlashPreview, CategoryVideoGeneration},
		{Speech28Turbo, CategorySpeech},
	}
	for _, tc := range cases {
		got, ok := CategoryOf(tc.id)
		if !ok {
			t.Fatalf("CategoryOf(%q) missing", tc.id)
		}
		if got != tc.want {
			t.Fatalf("CategoryOf(%q)=%q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestCategoryOfUnknown(t *testing.T) {
	if _, ok := CategoryOf("not-a-real-model"); ok {
		t.Fatal("unknown id must not be classified")
	}
}

func TestPublicCategoriesIncludeSpeech(t *testing.T) {
	if !CategoryLLM.Public() || !CategoryMultimodal.Public() || !CategoryImageGeneration.Public() || !CategoryVideoGeneration.Public() || !CategorySpeech.Public() {
		t.Fatal("llm/multimodal/image/video/speech must be public")
	}
	if Category("unknown").Public() {
		t.Fatal("unknown category must not be public")
	}
}
