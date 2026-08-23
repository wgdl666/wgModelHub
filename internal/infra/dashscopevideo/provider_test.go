package dashscopevideo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestNewUsesDefaultPollWhenZero(t *testing.T) {
	p, err := New("dashscope", "sk-test", "https://example.com", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.pollInterval != defaultPollInterval || p.maxPollTime != defaultMaxPollTime {
		t.Fatalf("poll=%v max=%v", p.pollInterval, p.maxPollTime)
	}
}

func TestProviderGenerateWanI2V(t *testing.T) {
	var createPayload map[string]any
	videoBody := []byte("mp4-bytes")
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services/aigc/video-generation/video-synthesis":
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"output":{"task_id":"tid-1"}}`))
		case "/tasks/tid-1":
			_, _ = w.Write([]byte(`{"output":{"task_status":"SUCCEEDED","results":{"video_url":"` + baseURL + `/video.mp4"}}}`))
		case "/video.mp4":
			_, _ = w.Write(videoBody)
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p, err := New("dashscope", "sk-test", server.URL, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	var chunks []*modelhubv2.GenerateEvent
	req := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Uri{Uri: "https://cdn.example/frame.png"},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{
			Resolution: "480p",
		}}},
	}
	err = p.GenerateVideo(context.Background(), models.Wan22I2VFlash, req, func(ev *modelhubv2.GenerateEvent) error {
		chunks = append(chunks, ev)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) == 0 || !chunks[len(chunks)-1].Final {
		t.Fatalf("chunks=%d", len(chunks))
	}
	if createPayload["model"] != models.Wan22I2VFlash {
		t.Fatalf("model=%v", createPayload["model"])
	}
	params, _ := createPayload["parameters"].(map[string]any)
	if params["prompt_extend"] != false {
		t.Fatalf("prompt_extend=%v", params["prompt_extend"])
	}
}

func TestProviderEditWanVideoEdit(t *testing.T) {
	var createPayload map[string]any
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services/aigc/video-generation/video-synthesis":
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			input, _ := createPayload["input"].(map[string]any)
			media, _ := input["media"].([]any)
			if len(media) < 2 {
				t.Fatalf("edit media order=%d", len(media))
			}
			first, _ := media[0].(map[string]any)
			if first["type"] != "video" {
				t.Fatalf("first media=%v", first)
			}
			_, _ = w.Write([]byte(`{"output":{"task_id":"edit-1"}}`))
		case "/tasks/edit-1":
			_, _ = w.Write([]byte(`{"output":{"task_status":"SUCCEEDED","video_url":"` + baseURL + `/out.mp4"}}`))
		case "/out.mp4":
			_, _ = w.Write([]byte("edited"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p, err := New("dashscope", "sk-test", server.URL, 0.001, 2)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	req := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Video{Video: &modelhubv2.Media{
						MimeType: "video/mp4",
						Source:   &modelhubv2.Media_Uri{Uri: "https://cdn.example/in.mp4"},
					}}},
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Uri{Uri: "https://cdn.example/ref.png"},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "swap"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{}}},
	}
	if err := p.GenerateVideo(context.Background(), models.Wan27VideoEdit, req, func(*modelhubv2.GenerateEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	params, _ := createPayload["parameters"].(map[string]any)
	if params["prompt_extend"] != true {
		t.Fatalf("edit prompt_extend=%v", params["prompt_extend"])
	}
}

func TestProviderRejectsGenerationWithVideoPart(t *testing.T) {
	p, err := New("dashscope", "sk", "https://example.com", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	req := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Video{Video: &modelhubv2.Media{
						Source: &modelhubv2.Media_Uri{Uri: "https://cdn.example/in.mp4"},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
				},
			}},
		}}},
	}
	err = p.GenerateVideo(context.Background(), models.Wan22I2VFlash, req, nil)
	if err == nil || !strings.Contains(err.Error(), "does not accept video") {
		t.Fatalf("err=%v", err)
	}
}

func TestProviderRejectsEditWithoutVideo(t *testing.T) {
	p, err := New("dashscope", "sk", "https://example.com", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	req := testGenRequest()
	err = p.GenerateVideo(context.Background(), models.Wan27VideoEdit, req, nil)
	if err == nil || !strings.Contains(err.Error(), "requires video") {
		t.Fatalf("err=%v", err)
	}
}

func TestProviderWan27I2VUsesMediaFirstFrame(t *testing.T) {
	var createPayload map[string]any
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services/aigc/video-generation/video-synthesis":
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"output":{"task_id":"w27"}}`))
		case "/tasks/w27":
			_, _ = w.Write([]byte(`{"output":{"task_status":"SUCCEEDED","video_url":"` + baseURL + `/out.mp4"}}`))
		case "/out.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p, err := New("dashscope", "sk-test", server.URL, 0.001, 2)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	if err := p.GenerateVideo(context.Background(), models.Wan27I2V, testGenRequest(), nil); err != nil {
		t.Fatal(err)
	}
	input, _ := createPayload["input"].(map[string]any)
	if _, ok := input["media"]; !ok {
		t.Fatalf("input=%v", input)
	}
}

func TestProviderPollFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/services/aigc/video-generation/video-synthesis" {
			_, _ = w.Write([]byte(`{"output":{"task_id":"fail-1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"output":{"task_status":"FAILED","code":"X","message":"bad"}}`))
	}))
	defer server.Close()
	p, err := New("dashscope", "sk", server.URL, 0.001, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	req := testGenRequest()
	err = p.GenerateVideo(context.Background(), models.Wan22I2VFlash, req, func(*modelhubv2.GenerateEvent) error { return nil })
	if err == nil {
		t.Fatal("expected poll failure")
	}
}

func testGenRequest() *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Uri{Uri: "https://cdn.example/frame.png"},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{}}},
	}
}

func TestWaitHonorsContextCancel(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/services/aigc/video-generation/video-synthesis" {
			_, _ = w.Write([]byte(`{"output":{"task_id":"tid"}}`))
			return
		}
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	p, _ := New("dashscope", "sk", server.URL, 0.05, 5)
	p.client = server.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.GenerateVideo(ctx, models.Wan22I2VFlash, testGenRequest(), func(*modelhubv2.GenerateEvent) error { return nil })
	if err == nil {
		t.Fatal("expected cancel/timeout")
	}
	close(block)
}
