package genai

import (
	"encoding/json"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

func TestSanitizeGeminiToolSchemaRemovesAdditionalPropertiesRecursively(t *testing.T) {
	raw := []byte(`{
		"type":"object",
		"additionalProperties":false,
		"required":["query","query_features"],
		"properties":{
			"query":{"type":"string","description":"语义检索"},
			"query_features":{
				"type":"array",
				"items":{"type":"string","minLength":1,"additionalProperties":false}
			},
			"precision":{"type":"string","enum":["high","low"]}
		}
	}`)
	var original map[string]any
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	got := sanitizeGeminiToolSchema(original).(map[string]any)
	if _, ok := got["additionalProperties"]; ok {
		t.Fatal("top-level additionalProperties must be removed")
	}
	if got["type"] != "object" {
		t.Fatalf("type = %#v", got["type"])
	}
	required, _ := got["required"].([]any)
	if len(required) != 2 || required[0] != "query" || required[1] != "query_features" {
		t.Fatalf("required = %#v", got["required"])
	}
	props := got["properties"].(map[string]any)
	query := props["query"].(map[string]any)
	if query["description"] != "语义检索" {
		t.Fatalf("query description lost: %#v", query)
	}
	features := props["query_features"].(map[string]any)
	items := features["items"].(map[string]any)
	if _, ok := items["additionalProperties"]; ok {
		t.Fatal("nested additionalProperties must be removed")
	}
	if items["minLength"] != float64(1) {
		t.Fatalf("minLength must be preserved: %#v", items["minLength"])
	}
	precision := props["precision"].(map[string]any)
	enumVals, _ := precision["enum"].([]any)
	if len(enumVals) != 2 || enumVals[0] != "high" {
		t.Fatalf("enum lost: %#v", precision["enum"])
	}
	// 原始输入不得被原地修改。
	if _, ok := original["additionalProperties"]; !ok {
		t.Fatal("original top-level additionalProperties was mutated")
	}
	origItems := original["properties"].(map[string]any)["query_features"].(map[string]any)["items"].(map[string]any)
	if _, ok := origItems["additionalProperties"]; !ok {
		t.Fatal("original nested additionalProperties was mutated")
	}
}

func TestBuildToolsSanitizesParametersWithoutMutatingSource(t *testing.T) {
	params := []byte(`{"type":"object","additionalProperties":false,"properties":{"q":{"type":"string"}},"required":["q"]}`)
	src := append([]byte(nil), params...)
	tools := buildTools([]*modelhubv2.Tool{{
		Function: &modelhubv2.FunctionDefinition{
			Name:                 "closet_search",
			ParametersJsonSchema: params,
		},
	}})
	if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	schema := tools[0].FunctionDeclarations[0].ParametersJsonSchema.(map[string]any)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatal("built tool still has additionalProperties")
	}
	if string(params) != string(src) {
		t.Fatal("ParametersJsonSchema bytes were mutated")
	}
	var decoded map[string]any
	if err := json.Unmarshal(params, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["additionalProperties"]; !ok {
		t.Fatal("source JSON lost additionalProperties after buildTools")
	}
}
