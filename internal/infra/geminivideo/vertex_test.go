package geminivideo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"net/http"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func TestVertexRequestAndValidation(t *testing.T) {
	p := NewVertex("vertex", "project", http.DefaultClient)
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
	if format["gcs_uri"] != nil || format["delivery"] != nil || format["duration"] != "5s" || format["resolution"] != "1080p" || got["background"] != true || got["store"] != true {
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
	if !strings.HasSuffix(p.interactionsURL(), "/projects/project/locations/global/interactions") {
		t.Fatal("wrong endpoint")
	}
}

func TestVertexInlineInvalidAndEmitterFailure(t *testing.T) {
	p := NewVertex("vertex", "project", http.DefaultClient)
	for _, data := range []string{"", "%%broken%%", "\n"} {
		err := p.readInlineVideo(context.Background(), data, "video/mp4", "job", 0, nil)
		if provider.Kind(err) != provider.ErrorInvalidResponse {
			t.Fatalf("data=%q err=%v", data, err)
		}
	}
	// 禁用 ffmpeg，使测试聚焦解码和下游取消传播。
	t.Setenv("PATH", t.TempDir())
	stopped := errors.New("consumer stopped")
	data := base64.StdEncoding.EncodeToString([]byte("video"))
	err := p.readInlineVideo(context.Background(), data, "video/mp4", "job", 0, func(*modelhubv2.GenerateEvent) error { return stopped })
	if !errors.Is(err, stopped) {
		t.Fatalf("emitter error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.readInlineVideo(ctx, data, "video/mp4", "job", 0, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
}
