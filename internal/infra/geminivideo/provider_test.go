package geminivideo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

func testI2VRequest() *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Data{Data: []byte{0x89, 0x50, 0x4e, 0x47}},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{
			AspectRatio: ptr("9:16"),
		}}},
	}
}

func ptr(s string) *string { return &s }

func newTestProvider(baseURL string, client *http.Client) *Provider {
	p, err := New("gemini", "sk-test", baseURL, "", 0)
	if err != nil {
		panic(err)
	}
	p.client = client
	return p
}

func TestNewUsesDefaultPollWhenZero(t *testing.T) {
	p, err := New("gemini", "sk-test", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.pollInterval != defaultPollInterval {
		t.Fatalf("poll=%v", p.pollInterval)
	}
}

func TestGenerateI2VInteractionPayload(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1beta/interactions":
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{
				"id":"int-1",
				"status":"completed",
				"created":"2026-01-01T00:00:00Z",
				"updated":"2026-01-01T00:00:05Z",
				"output_video":{"type":"video","mime_type":"video/mp4","data":"` + base64.StdEncoding.EncodeToString([]byte("mp4-bytes")) + `"}
			}`))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p := newTestProvider(server.URL+"/v1beta", server.Client())
	var final *modelhubv2.GenerateEvent
	err := p.GenerateVideo(context.Background(), models.GeminiOmniFlashPreview, testI2VRequest(), func(ev *modelhubv2.GenerateEvent) error {
		if ev.GetFinal() {
			final = ev
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if final == nil || final.GetResponseId() != "int-1" || final.GetGenerationElapsedMs() <= 0 {
		t.Fatalf("final=%#v", final)
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input=%v", body["input"])
	}
}

func TestGenerateI2VURIOutputDownloadsWithAuth(t *testing.T) {
	const fileID = "out-file"
	const interactionID = "int-uri"
	var apiBase string
	var sawFilePoll, sawAuthDownload bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1beta/interactions":
			downloadURI := apiBase + "/v1beta/files/" + fileID + "/content"
			_, _ = w.Write([]byte(`{
				"id":"` + interactionID + `",
				"status":"completed",
				"created":"2026-01-01T00:00:00Z",
				"updated":"2026-01-01T00:00:10Z",
				"output_video":{"type":"video","mime_type":"video/mp4","uri":"` + downloadURI + `"}
			}`))
		case r.URL.Path == "/v1beta/files/"+fileID:
			sawFilePoll = true
			_, _ = w.Write([]byte(`{"state":"ACTIVE"}`))
		case r.URL.Path == "/v1beta/files/"+fileID+"/content":
			if r.Header.Get("x-goog-api-key") != "sk-test" {
				t.Fatalf("missing auth on download")
			}
			sawAuthDownload = true
			_, _ = w.Write([]byte("uri-mp4-bytes"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	apiBase = server.URL

	p := newTestProvider(server.URL+"/v1beta", server.Client())
	var final *modelhubv2.GenerateEvent
	var gotBytes []byte
	err := p.GenerateVideo(context.Background(), models.GeminiOmniFlashPreview, testI2VRequest(), func(ev *modelhubv2.GenerateEvent) error {
		if item := ev.GetItems(); len(item) > 0 {
			if v := item[0].GetVideo(); v != nil {
				gotBytes = append(gotBytes, v.GetData()...)
			}
		}
		if ev.GetFinal() {
			final = ev
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawFilePoll || !sawAuthDownload {
		t.Fatalf("poll=%v download=%v", sawFilePoll, sawAuthDownload)
	}
	if string(gotBytes) != "uri-mp4-bytes" || final == nil || final.GetResponseId() != interactionID || !final.GetFinal() {
		t.Fatalf("bytes=%q final=%#v", gotBytes, final)
	}
}

func TestGenerateEditUploadsFilesAndOrdersParts(t *testing.T) {
	var interactionBody map[string]any
	var uploadFinalize bool
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files") && r.Header.Get("X-Goog-Upload-Command") == "start":
			w.Header().Set("X-Goog-Upload-URL", serverURL+"/upload/finalize")
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/upload/finalize"):
			uploadFinalize = true
			_, _ = w.Write([]byte(`{"file":{"name":"files/vid123","uri":"https://generativelanguage.googleapis.com/v1beta/files/vid123"}}`))
		case strings.Contains(r.URL.Path, "/files/vid123"):
			_, _ = w.Write([]byte(`{"state":"ACTIVE"}`))
		case r.URL.Path == "/v1beta/interactions":
			_ = json.NewDecoder(r.Body).Decode(&interactionBody)
			_, _ = w.Write([]byte(`{
				"id":"edit-1",
				"status":"completed",
				"output_video":{"type":"video","mime_type":"video/mp4","data":"` + base64.StdEncoding.EncodeToString([]byte("edited")) + `"}
			}`))
		default:
			t.Fatalf("path %s cmd=%s", r.URL.Path, r.Header.Get("X-Goog-Upload-Command"))
		}
	}))
	defer server.Close()
	serverURL = server.URL

	req := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Video{Video: &modelhubv2.Media{
						MimeType: "video/mp4",
						Source:   &modelhubv2.Media_Data{Data: []byte("source-video")},
					}}},
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Data{Data: []byte{0x89, 0x50}},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "swap outfit"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{}}},
	}
	p := newTestProvider(server.URL+"/v1beta", server.Client())
	if err := p.GenerateVideo(context.Background(), models.GeminiOmniFlashPreview, req, func(*modelhubv2.GenerateEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !uploadFinalize {
		t.Fatal("expected files upload finalize")
	}
	input, ok := interactionBody["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("input=%v", interactionBody["input"])
	}
	first, _ := input[0].(map[string]any)
	if first["type"] != "video" {
		t.Fatalf("first part=%v", first)
	}
	genCfg, _ := interactionBody["generation_config"].(map[string]any)
	videoCfg, _ := genCfg["video_config"].(map[string]any)
	if videoCfg["task"] != "edit" {
		t.Fatalf("task=%v", videoCfg)
	}
}

func TestLoadMediaBytesEnforcesCallerMaxBytes(t *testing.T) {
	p, err := New("gemini", "sk", "https://example.com/v1beta", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	// 编辑源视频走 MaxVideoBytes；图片仍走 MaxMediaBytes，用小上限断言边界而不分配 200MiB。
	_, _, err = p.loadMediaBytes(context.Background(), &modelhubv2.Media{
		MimeType: "video/mp4",
		Source:   &modelhubv2.Media_Data{Data: make([]byte, 64)},
	}, 32)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err=%v", err)
	}
	_, _, err = p.loadMediaBytes(context.Background(), &modelhubv2.Media{
		MimeType: "video/mp4",
		Source:   &modelhubv2.Media_Data{Data: make([]byte, 32)},
	}, protocol.MaxVideoBytes)
	if err != nil {
		t.Fatalf("expected video under MaxVideoBytes to pass: %v", err)
	}
}

func TestGenerateHonorsContextCancel(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1beta/interactions" {
			select {
			case <-block:
			case <-r.Context().Done():
			}
		}
	}))
	defer server.Close()

	p := newTestProvider(server.URL+"/v1beta", server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.GenerateVideo(ctx, models.GeminiOmniFlashPreview, testI2VRequest(), nil)
	if err == nil {
		t.Fatal("expected cancel")
	}
	close(block)
}
