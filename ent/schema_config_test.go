package ent_test

import (
	"testing"

	"github.com/wgdl666/wgModelHub/ent"
)

func TestDefaultSchemaConfigQualifiesModelhubTables(t *testing.T) {
	if got := ent.DefaultSchemaConfig.GenerationTask; got != "modelhub" {
		t.Fatalf("GenerationTask schema=%q, want modelhub", got)
	}
	if got := ent.DefaultSchemaConfig.ModelhubAPIKey; got != "modelhub" {
		t.Fatalf("ModelhubAPIKey schema=%q, want modelhub", got)
	}
}
