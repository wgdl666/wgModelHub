package models

import "testing"

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

func TestGemini37FlashConstant(t *testing.T) {
	if Gemini37Flash != "gemini-3.7-flash" {
		t.Fatalf("Gemini37Flash = %q", Gemini37Flash)
	}
	found := false
	for _, model := range All() {
		if model == Gemini37Flash {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Gemini37Flash missing from All()")
	}
}
