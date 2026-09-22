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
	if _, exists := schema["id"]; exists {
		t.Fatal("AppConfig inline schema must not set an external id")
	}
	if _, exists := schema["$id"]; exists {
		t.Fatal("draft-04 schema must use id, not $id")
	}
	assertStringSet(t, schema["required"], "database", "providers", "logfire")

	// 仓库内 schema 是本地契约/测试对照，不自动绑定云端 AppConfig Validator；与进程 YAML 解析一致，允许未知字段。
	if ap, exists := schema["additionalProperties"]; exists && ap == false {
		t.Fatal("root must allow unknown fields")
	}
	definitions := mustMap(t, schema["definitions"])
	for _, definitionName := range []string{
		"database", "logfire", "provider", "gemini", "ark", "openai", "ltx",
		"dashscopeVideo", "ominilinkVideo", "geminiVideo", "arkVideo", "photoroom",
		"minimaxTts", "elevenlabsTts", "segmentPerson",
	} {
		def := mustMap(t, definitions[definitionName])
		if ap, exists := def["additionalProperties"]; exists && ap == false {
			t.Fatalf("%s must allow unknown fields", definitionName)
		}
	}

	// providers / model_routes 的 additionalProperties:false 配合 patternProperties，约束动态 map 键名（非空白首尾），不是固定 struct 未知字段限制。
	properties := mustMap(t, schema["properties"])
	for _, mapName := range []string{"providers", "model_routes"} {
		mapSchema := mustMap(t, properties[mapName])
		if mapSchema["additionalProperties"] != false {
			t.Fatalf("%s.additionalProperties=%v, want false (map key pattern gate)", mapName, mapSchema["additionalProperties"])
		}
		patterns := mustMap(t, mapSchema["patternProperties"])
		if _, exists := patterns[`^\S(?:.*\S)?$`]; !exists {
			t.Fatalf("%s.patternProperties missing non-blank key pattern", mapName)
		}
	}

	provider := mustMap(t, definitions["provider"])
	assertStringSet(t, provider["required"], "models")
	oneOf := mustSlice(t, provider["oneOf"])
	wantKinds := map[string]bool{
		"gemini": false, "vertexai": false, "ark": false, "openai": false,
		"ltx": false, "dashscope_video": false, "ominilink_video": false,
		"gemini_video": false, "ark_video": false, "minimax_tts": false,
		"elevenlabs_tts": false, "photoroom": false, "segment_person": false,
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
		"ominilinkVideo", "geminiVideo", "arkVideo", "photoroom", "segmentPerson", "logfire", "database",
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

	arkProperties := mustMap(t, mustMap(t, definitions["ark"])["properties"])
	endpointID := mustMap(t, arkProperties["endpoint_id"])
	if endpointID["type"] != "string" {
		t.Fatalf("ark.endpoint_id type=%v, want string", endpointID["type"])
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
