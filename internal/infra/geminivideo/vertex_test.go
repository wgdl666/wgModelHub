package geminivideo

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func TestVertexRequestAndValidation(t *testing.T) {
	p := NewVertex("vertex", "project", "gs://bucket/out/", http.DefaultClient)
	req := testI2VRequest()
	duration := int32(5)
	req.GetOutput().GetVideo().DurationSeconds = &duration
	req.GetOutput().GetVideo().Resolution = "1080p"
	body, err := p.buildVertexBody(context.Background(), models.GeminiOmni11FlashPreview, req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	formats := got["response_format"].([]any)
	format := formats[0].(map[string]any)
	if format["gcs_uri"] != "gs://bucket/out/" || format["duration"] != "5s" || format["resolution"] != "1080p" || got["background"] != true || got["store"] != true {
		t.Fatalf("request=%s", body)
	}
	input := got["input"].([]any)[0].(map[string]any)
	if input["type"] != "user_input" || len(input["content"].([]any)) != 2 {
		t.Fatalf("input=%v", input)
	}
	if _, err := p.buildVertexBody(context.Background(), models.GeminiOmniFlashPreview, req); err == nil {
		t.Fatal("old Omni must reject 1080p")
	}
	duration = 11
	if _, err := p.buildVertexBody(context.Background(), models.GeminiOmni11FlashPreview, req); err == nil {
		t.Fatal("must not silently clamp duration")
	}
	duration = 5
	p.vertexOutputURI = ""
	if _, err := p.buildVertexBody(context.Background(), models.GeminiOmni11FlashPreview, req); err == nil {
		t.Fatal("missing GCS must fail before submit")
	}
}

func TestVertexDownloadGCSObject(t *testing.T) {
	got, err := vertexDownloadURL("gs://bucket/nested/a b.mp4")
	if err != nil || got != "https://storage.googleapis.com/storage/v1/b/bucket/o/nested%2Fa%20b.mp4?alt=media" {
		t.Fatalf("url=%q err=%v", got, err)
	}
	for _, uri := range []string{"gs://bucket/", "https://example.com/video.mp4", "gs://user@bucket/file", "gs://bucket/file?token=secret"} {
		if _, err := vertexDownloadURL(uri); err == nil {
			t.Fatalf("accepted invalid URI %q", uri)
		}
	}
	if !strings.HasSuffix(NewVertex("v", "project", "", http.DefaultClient).interactionsURL(), "/projects/project/locations/global/interactions") {
		t.Fatal("wrong endpoint")
	}
}
