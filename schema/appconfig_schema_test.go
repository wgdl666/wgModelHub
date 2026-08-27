package schema_test

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAppConfigSchemaContract(t *testing.T) {
	content, err := os.ReadFile("appconfig.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(content, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "http://json-schema.org/draft-04/schema#" {
		t.Fatalf("$schema=%v", schema["$schema"])
	}
	if schema["id"] != "https://wgdl.tech/schemas/modelhub/appconfig.schema.json" {
		t.Fatalf("id=%v", schema["id"])
	}
	if _, exists := schema["$id"]; exists {
		t.Fatal("draft-04 schema must use id, not $id")
	}
	assertStringSet(t, schema["required"], "database", "providers", "logfire")

	definitions := mustMap(t, schema["definitions"])
	provider := mustMap(t, definitions["provider"])
	assertStringSet(t, provider["required"], "models")
	oneOf := mustSlice(t, provider["oneOf"])
	wantKinds := map[string]bool{
		"gemini": false, "vertexai": false, "ark": false, "openai": false,
		"ltx": false, "dashscope_video": false, "ominilink_video": false,
		"gemini_video": false, "ark_video": false,
	}
	for _, entry := range oneOf {
		required := mustSlice(t, mustMap(t, entry)["required"])
		if len(required) != 1 {
			t.Fatalf("provider oneOf required=%v", required)
		}
		kind, ok := required[0].(string)
		if !ok {
			t.Fatalf("provider kind=%T", required[0])
		}
		if _, exists := wantKinds[kind]; !exists {
			t.Fatalf("unexpected provider kind %q", kind)
		}
		wantKinds[kind] = true
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("provider kind %q missing from oneOf", kind)
		}
	}

	for _, definitionName := range []string{
		"gemini", "ark", "openai", "ltx", "dashscopeVideo",
		"ominilinkVideo", "geminiVideo", "arkVideo", "logfire", "database",
	} {
		properties := mustMap(t, mustMap(t, definitions[definitionName])["properties"])
		for field, raw := range properties {
			if field == "duration" || field == "fps" || field == "seed" || field == "poll_interval" || field == "max_poll_time" {
				continue
			}
			property := mustMap(t, raw)
			if property["type"] != "string" {
				t.Errorf("%s.%s type=%v, want string", definitionName, field, property["type"])
			}
		}
	}
}

func mustMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value=%T, want object", value)
	}
	return result
}

func mustSlice(t *testing.T, value any) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("value=%T, want array", value)
	}
	return result
}

func assertStringSet(t *testing.T, value any, want ...string) {
	t.Helper()
	values := mustSlice(t, value)
	got := make(map[string]bool, len(values))
	for _, item := range values {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("set item=%T, want string", item)
		}
		got[text] = true
	}
	if len(got) != len(want) {
		t.Fatalf("set=%v, want=%v", got, want)
	}
	for _, item := range want {
		if !got[item] {
			t.Fatalf("set=%v missing %q", got, item)
		}
	}
}
