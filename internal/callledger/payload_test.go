package callledger

import (
	"encoding/base64"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

func TestTextAccumulatorKeepsTwoParallelTools(t *testing.T) {
	acc := NewTextAccumulator()
	idx0 := int32(0)
	idx1 := int32(1)
	acc.Consume(&modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{
		{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{
			Id: "a", Name: "search", Index: &idx0, ArgumentsJson: []byte(`{"q":`),
		}}},
		{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{
			Id: "b", Name: "lookup", Index: &idx1, ArgumentsJson: []byte(`{"id":`),
		}}},
	}})
	acc.Consume(&modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{
		{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{
			Id: "a", Index: &idx0, ArgumentsJson: []byte(`"hi"}`), ThoughtSignature: []byte("sig"),
		}}},
		{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{
			Id: "b", Index: &idx1, ArgumentsJson: []byte(`1}`),
		}}},
	}})
	out := BuildTextOutput(acc, nil)
	tools, _ := out["tool_calls"].([]map[string]any)
	if len(tools) != 2 {
		t.Fatalf("tools=%v", out["tool_calls"])
	}
	if tools[0]["arguments_json"] != `{"q":"hi"}` || tools[1]["arguments_json"] != `{"id":1}` {
		t.Fatalf("args=%v %v", tools[0]["arguments_json"], tools[1]["arguments_json"])
	}
	if tools[0]["thought_signature"] != base64.StdEncoding.EncodeToString([]byte("sig")) {
		t.Fatalf("signature must be base64: %v", tools[0])
	}
	if tools[0]["index"] != int32(0) && tools[0]["index"] != float64(0) {
		// map values may be int32
		if v, ok := tools[0]["index"].(int32); !ok || v != 0 {
			t.Fatalf("index0=%v", tools[0]["index"])
		}
	}
}

func TestTextAccumulatorThoughtSignatureNonUTF8Base64(t *testing.T) {
	acc := NewTextAccumulator()
	raw := []byte{0xff, 0xfe, 0x00, 0x80}
	acc.Consume(&modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{
		{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{
			Id: "a", Name: "t", ThoughtSignature: raw,
		}}},
	}})
	out := BuildTextOutput(acc, nil)
	tools, _ := out["tool_calls"].([]map[string]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%v", out)
	}
	want := base64.StdEncoding.EncodeToString(raw)
	if tools[0]["thought_signature"] != want {
		t.Fatalf("got=%v want=%s", tools[0]["thought_signature"], want)
	}
}

func TestBuildImageOutputThoughtSignatureBase64(t *testing.T) {
	raw := []byte{0xff, 0x00}
	payload, _ := BuildImageOutput(&modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{
		{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{
			Id: "x", Name: "n", ThoughtSignature: raw,
		}}},
	}})
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items=%v", items)
	}
	entry := items[0].(map[string]any)
	tc := entry["tool_call"].(map[string]any)
	if tc["thought_signature"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("sig=%v", tc["thought_signature"])
	}
}

func TestSanitizeErrorMessageRedactsBearerSecret(t *testing.T) {
	got := sanitizeErrorMessage(`upstream Authorization: Bearer super-secret-token failed`)
	if strings.Contains(got, "super-secret-token") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("expected redaction: %q", got)
	}
	got = sanitizeErrorMessage(`api_key=abc123xyz remaining`)
	if strings.Contains(got, "abc123xyz") {
		t.Fatalf("api key leaked: %q", got)
	}
}

func TestRequestImageSpecNoFakeQuality(t *testing.T) {
	aspect := "3:4"
	size := "1K"
	req := &modelhubv2.GenerateRequest{
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{
			ImageSize:   &size,
			AspectRatio: &aspect,
		}}},
	}
	gotSize, gotAspect := RequestImageSpec(req)
	if gotSize != "1K" || gotAspect != "3:4" {
		t.Fatalf("size=%q aspect=%q", gotSize, gotAspect)
	}
}
