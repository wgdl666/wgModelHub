package arkvideo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func testTextRequest(duration int32, aspect string) *modelhubv2.GenerateRequest {
	out := &modelhubv2.VideoOutput{Resolution: "720p"}
	if duration != 0 {
		out.DurationSeconds = &duration
	}
	if aspect != "" {
		out.AspectRatio = &aspect
	}
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Text{Text: "a cat walks on the street"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: out}},
	}
}

func testGenRequest(imageURL string, duration int32, aspect string) *modelhubv2.GenerateRequest {
	out := &modelhubv2.VideoOutput{
		Resolution:  "4k",
		AspectRatio: &aspect,
	}
	if duration != 0 {
		out.DurationSeconds = &duration
	}
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Uri{Uri: imageURL},
					}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: out}},
	}
}

func TestNewUsesDefaultPollWhenZero(t *testing.T) {
	p, err := New("ark_video", "sk-test", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.pollInterval != defaultPollInterval || p.maxPollTime != defaultMaxPollTime {
		t.Fatalf("poll=%v max=%v", p.pollInterval, p.maxPollTime)
	}
	if p.baseURL != defaultBaseURL {
		t.Fatalf("baseURL=%s", p.baseURL)
	}
}

func TestGenerateSeedance25Payload(t *testing.T) {
	const taskID = "cgt-seedance-25"
	var createPayload map[string]any
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == createPath:
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"id":"` + taskID + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == createPath+"/"+taskID:
			_, _ = w.Write([]byte(`{"id":"` + taskID + `","status":"succeeded","content":{"video_url":"` + baseURL + `/out.mp4"}}`))
		case r.URL.Path == "/out.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p, err := New("ark_video", "sk-test", server.URL, 0.001, 2)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	var final *modelhubv2.GenerateEvent
	var gotBytes []byte
	if err := p.GenerateVideo(context.Background(), models.DoubaoSeedance25, testGenRequest("https://cdn.example/frame.png", 40, "3:4"), func(ev *modelhubv2.GenerateEvent) error {
		if items := ev.GetItems(); len(items) > 0 {
			if v := items[0].GetVideo(); v != nil {
				gotBytes = append(gotBytes, v.GetData()...)
			}
		}
		if ev.GetFinal() {
			final = ev
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(gotBytes) != "mp4" || final == nil || !final.GetFinal() || final.GetResponseId() != taskID {
		t.Fatalf("bytes=%q final=%#v", gotBytes, final)
	}
	if createPayload["model"] != models.DoubaoSeedance25 {
		t.Fatalf("model=%v", createPayload["model"])
	}
	if createPayload["ratio"] != "adaptive" {
		t.Fatalf("ratio=%v, first_frame must be adaptive", createPayload["ratio"])
	}
	if createPayload["resolution"] != "1080p" {
		t.Fatalf("resolution=%v, 4k should downshift to 1080p", createPayload["resolution"])
	}
	if createPayload["duration"] != float64(30) {
		t.Fatalf("duration=%v, 2.5 max is 30", createPayload["duration"])
	}
	content, _ := createPayload["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content=%v", createPayload["content"])
	}
	imageBlock, _ := content[1].(map[string]any)
	if imageBlock["role"] != "first_frame" {
		t.Fatalf("image role=%v", imageBlock["role"])
	}
}

func TestSubmitRejectsVideoInput(t *testing.T) {
	p, err := New("ark_video", "sk", "https://example.com", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	req := testGenRequest("https://cdn.example/frame.png", 8, "adaptive")
	req.Input.Items[0].GetMessage().Parts = append(req.Input.Items[0].GetMessage().Parts, &modelhubv2.ContentPart{
		Content: &modelhubv2.ContentPart_Video{Video: &modelhubv2.Media{
			MimeType: "video/mp4",
			Source:   &modelhubv2.Media_Uri{Uri: "https://cdn.example/in.mp4"},
		}},
	})
	_, err = p.SubmitVideo(context.Background(), models.DoubaoSeedance25, req)
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument || !strings.Contains(err.Error(), "does not accept video input") {
		t.Fatalf("err=%v", err)
	}
}

func TestSubmitTextToVideoPayload(t *testing.T) {
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&createPayload)
		_, _ = w.Write([]byte(`{"id":"cgt-t2v"}`))
	}))
	defer server.Close()
	p, err := New("ark_video", "sk-test", server.URL, 0.01, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	id, err := p.SubmitVideo(context.Background(), models.DoubaoSeedance25, testTextRequest(12, "9:16"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "cgt-t2v" {
		t.Fatalf("id=%s", id)
	}
	if createPayload["model"] != models.DoubaoSeedance25 {
		t.Fatalf("model=%v", createPayload["model"])
	}
	if createPayload["ratio"] != "9:16" {
		t.Fatalf("ratio=%v, T2V must keep caller aspect", createPayload["ratio"])
	}
	if createPayload["duration"] != float64(12) {
		t.Fatalf("duration=%v", createPayload["duration"])
	}
	content, _ := createPayload["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content=%v", createPayload["content"])
	}
	textBlock, _ := content[0].(map[string]any)
	if textBlock["type"] != "text" || textBlock["text"] == "" {
		t.Fatalf("text block=%v", textBlock)
	}
}

func TestSubmitRequiresPromptText(t *testing.T) {
	p, err := New("ark_video", "sk", "https://example.com", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	req := &modelhubv2.GenerateRequest{
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{}}},
	}
	_, err = p.SubmitVideo(context.Background(), models.DoubaoSeedance25, req)
	if err == nil || !strings.Contains(err.Error(), "prompt text") {
		t.Fatalf("err=%v", err)
	}
}

func TestGetVideoMapsFailedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"cgt-1","status":"failed","error":{"code":"InputImageSensitiveContentDetected","message":"blocked"}}`))
	}))
	defer server.Close()
	p, err := New("ark_video", "sk", server.URL, 0.01, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	job, err := p.GetVideo(context.Background(), models.DoubaoSeedance25, "cgt-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != provider.VideoJobFailed || job.Err == nil || !strings.Contains(job.Err.Error(), "blocked") {
		t.Fatalf("job=%#v", job)
	}
}

func TestCreateHTTPErrorIncludesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"InvalidParameter","message":"ratio must be adaptive"}}`))
	}))
	defer server.Close()
	p, err := New("ark_video", "sk", server.URL, 0.01, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	_, err = p.SubmitVideo(context.Background(), models.DoubaoSeedance25, testGenRequest("https://cdn.example/frame.png", 8, "3:4"))
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument || !strings.Contains(err.Error(), "ratio must be adaptive") {
		t.Fatalf("err=%v", err)
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	if _, err := New("ark_video", "  ", "", 1, 1); err == nil {
		t.Fatal("expected api_key error")
	}
}

func TestPollTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"id":"cgt-slow"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"cgt-slow","status":"running"}`))
	}))
	defer server.Close()
	p, err := New("ark_video", "sk", server.URL, 0.01, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	err = p.GenerateVideo(context.Background(), models.DoubaoSeedance25, testGenRequest("https://cdn.example/frame.png", 5, "adaptive"), nil)
	if err == nil || provider.Kind(err) != provider.ErrorTimeout {
		t.Fatalf("err=%v", err)
	}
}
