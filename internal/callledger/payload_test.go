package callledger

import (
	"context"
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
	payload, _, _ := BuildImageOutput(&modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{
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

func TestVideoArchiveRecordsBucketAndKey(t *testing.T) {
	store := &putOnce{}
	got := ArchiveDownloadedVideo(context.Background(), store, "call-9", 1, 5, "video/mp4", 2, []Blob{
		{MIME: "video/mp4", Data: []byte("abc")},
		{MIME: "video/mp4", Data: []byte("de")},
	})
	if got["bucket"] != "bucket" || got["object_key"] != "model-calls/call-9/video.mp4" {
		t.Fatalf("location=%v %v", got["bucket"], got["object_key"])
	}
	if _, ok := got["uri"]; ok {
		t.Fatalf("playback record is bucket and object_key, not uri: %v", got["uri"])
	}
	if store.key != "model-calls/call-9/video.mp4" || string(store.body) != "abcde" {
		t.Fatalf("uploaded key=%s body=%q", store.key, store.body)
	}
	if _, ok := got["media_archive"]; ok {
		t.Fatalf("archived video must drop the summary-only note: %v", got)
	}
	if got["chunk_count"] != 2 || got["mime_type"] != "video/mp4" {
		t.Fatalf("payload=%v", got)
	}
	if FirstMediaKey(got) != "model-calls/call-9/video.mp4" {
		t.Fatalf("key=%s", FirstMediaKey(got))
	}
}

func TestVideoArchiveUploadFailureStaysSummary(t *testing.T) {
	got := ArchiveDownloadedVideo(context.Background(), nil, "call-9", 1, 2, "video/mp4", 1, []Blob{{MIME: "video/mp4", Data: []byte("ab")}})
	if got["bucket"] != nil || got["object_key"] != nil {
		t.Fatalf("failed upload must not record a location: %v", got)
	}
	if got["media_archive"] != mediaArchiveNote || FirstMediaKey(got) != "" {
		t.Fatalf("payload=%v key=%s", got, FirstMediaKey(got))
	}
}
