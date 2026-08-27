package schema

import (
	"testing"

	"entgo.io/ent/dialect/entsql"
)

func TestModelhubAPIKeyUsesModelhubSchema(t *testing.T) {
	annotations := (ModelhubAPIKey{}).Annotations()
	if len(annotations) != 1 {
		t.Fatalf("annotations=%d, want 1", len(annotations))
	}
	annotation, ok := annotations[0].(entsql.Annotation)
	if !ok {
		t.Fatalf("annotation type=%T, want entsql.Annotation", annotations[0])
	}
	if annotation.Table != "modelhub_api_key" || annotation.Schema != "modelhub" {
		t.Fatalf("table=%q schema=%q", annotation.Table, annotation.Schema)
	}
}
