package ominilinkvideo

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

func testGenRequest(imageURL string) *modelhubv2.GenerateRequest {
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
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{
			Resolution: "720p",
		}}},
	}
}

func newTestProvider(server *httptest.Server) *Provider {
	p, err := New("ominilink", "sk-test", server.URL, 0, 0)
	if err != nil {
		panic(err)
	}
	p.client = server.Client()
	return p
}

func TestNewUsesDefaultPollWhenZero(t *testing.T) {
	p, err := New("ominilink", "sk-test", "https://example.com", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.pollInterval != defaultPollInterval || p.maxPollTime != defaultMaxPollTime {
		t.Fatalf("poll=%v max=%v", p.pollInterval, p.maxPollTime)
	}
}

func TestGenerateSeedancePayload(t *testing.T) {
	const model = models.DreaminaSeedance20
	const taskID = "seed-task"
	var createPayload map[string]any
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/"+model:
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"id":"` + taskID + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/query/"+model+"/"+taskID:
			_, _ = w.Write([]byte(`{"status":"success","video_url":"` + baseURL + `/vod/out.mp4"}`))
		case r.URL.Path == "/vod/out.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p := newTestProvider(server)
	var final *modelhubv2.GenerateEvent
	var gotBytes []byte
	if err := p.GenerateVideo(context.Background(), model, testGenRequest("https://cdn.example/frame.png"), func(ev *modelhubv2.GenerateEvent) error {
		if item := ev.GetItems(); len(item) > 0 {
			if v := item[0].GetVideo(); v != nil {
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
	if createPayload["ratio"] == nil {
		t.Fatalf("payload=%v", createPayload)
	}
}

func TestGenerateKlingUsesImage2VideoPath(t *testing.T) {
	const model = models.KlingV3
	const taskID = "kling-job"
	var baseURL string
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/image2video"):
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"Response":{"JobId":"` + taskID + `"}}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/query/"):
			_, _ = w.Write([]byte(`{"status":"success","result_url":"` + baseURL + `/kling.mp4"}`))
		case r.URL.Path == "/kling.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p := newTestProvider(server)
	if err := p.GenerateVideo(context.Background(), model, testGenRequest("https://cdn.example/frame.png"), nil); err != nil {
		t.Fatal(err)
	}
	if createPayload["Model"] != "v3.0" {
		t.Fatalf("Model=%v", createPayload["Model"])
	}
}

func TestGenerateViduUsesImg2VideoPath(t *testing.T) {
	const model = models.ViduQ3ProFast
	const taskID = "vidu-task"
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/img2video"):
			_, _ = w.Write([]byte(`{"task_id":"` + taskID + `"}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/query/"):
			_, _ = w.Write([]byte(`{"state":"success","video_url":"` + baseURL + `/vidu.mp4"}`))
		case r.URL.Path == "/vidu.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	p := newTestProvider(server)
	if err := p.GenerateVideo(context.Background(), model, testGenRequest("https://cdn.example/frame.png"), nil); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateVeoUsesInstancesPayload(t *testing.T) {
	const model = models.Veo31Generate001
	const taskID = "veo-task"
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer imageServer.Close()

	var createPayload map[string]any
	var apiBase string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, model):
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"name":"` + taskID + `"}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/query/"):
			_, _ = w.Write([]byte(`{"status":"success","video_url":"` + apiBase + `/veo.mp4"}`))
		case r.URL.Path == "/veo.mp4":
			_, _ = w.Write([]byte("mp4"))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer apiServer.Close()
	apiBase = apiServer.URL

	p := newTestProvider(apiServer)
	req := testGenRequest(imageServer.URL + "/frame.png")
	if err := p.GenerateVideo(context.Background(), model, req, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := createPayload["instances"]; !ok {
		t.Fatalf("payload=%v", createPayload)
	}
}

func TestGenerateRejectsVideoPart(t *testing.T) {
	p, err := New("ominilink", "sk", "https://example.com", 1, 1)
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
					{Content: &modelhubv2.ContentPart_Text{Text: "edit"}},
				},
			}},
		}}},
	}
	err = p.GenerateVideo(context.Background(), models.KlingV3, req, nil)
	if err == nil || !strings.Contains(err.Error(), "does not accept video") {
		t.Fatalf("err=%v", err)
	}
}

func TestGeneratePollFailed(t *testing.T) {
	const model = models.KlingV3
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"Response":{"JobId":"fail-1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"failed","message":"bad"}`))
	}))
	defer server.Close()

	p := newTestProvider(server)
	err := p.GenerateVideo(context.Background(), model, testGenRequest("https://cdn.example/frame.png"), nil)
	if err == nil {
		t.Fatal("expected failure")
	}
}

func TestGenerateHonorsContextCancel(t *testing.T) {
	block := make(chan struct{})
	const model = models.KlingV3
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"Response":{"JobId":"tid"}}`))
			return
		}
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	p := newTestProvider(server)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := p.GenerateVideo(ctx, model, testGenRequest("https://cdn.example/frame.png"), nil)
	if err == nil {
		t.Fatal("expected cancel")
	}
	close(block)
}
