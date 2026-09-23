package schema

import (
	"testing"

	"entgo.io/ent/dialect/entsql"
)

func TestModelCallAnnotationsQualifyModelhubSchema(t *testing.T) {
	anns := ModelCall{}.Annotations()
	if len(anns) != 1 {
		t.Fatalf("annotations=%d", len(anns))
	}
	raw, ok := anns[0].(entsql.Annotation)
	if !ok {
		t.Fatalf("annotation type=%T", anns[0])
	}
	if raw.Schema != "modelhub" || raw.Table != "model_call" {
		t.Fatalf("schema=%q table=%q", raw.Schema, raw.Table)
	}
}
