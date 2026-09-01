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
