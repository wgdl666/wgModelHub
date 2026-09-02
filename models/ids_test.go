package models

import (
	"slices"
	"testing"
)

// TestDoubaoSeed21ProModelID 锁定 2.1 Pro 对外真实模型名，避免与 2.0 mini/lite 或基础模型 ID 混用。
func TestDoubaoSeed21ProModelID(t *testing.T) {
	if DoubaoSeed21Pro != "doubao-seed-2.1-pro" {
		t.Fatalf("DoubaoSeed21Pro=%q, want doubao-seed-2.1-pro", DoubaoSeed21Pro)
	}
	if !slices.Contains(All(), DoubaoSeed21Pro) {
		t.Fatalf("All() missing %q", DoubaoSeed21Pro)
	}
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

func TestDoubaoSeed20Constants(t *testing.T) {
	if DoubaoSeed20Mini != "doubao-seed-2.0-mini" {
		t.Fatalf("DoubaoSeed20Mini = %q", DoubaoSeed20Mini)
	}
	if DoubaoSeed20Lite != "doubao-seed-2.0-lite" {
		t.Fatalf("DoubaoSeed20Lite = %q", DoubaoSeed20Lite)
	}
	for _, want := range []string{DoubaoSeed20Mini, DoubaoSeed20Lite} {
		found := false
		for _, model := range All() {
			if model == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s missing from All()", want)
		}
	}
}
