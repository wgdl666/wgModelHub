package ominilinkvideo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func TestReadViduCreationsResponse(t *testing.T) {
	raw, err := os.ReadFile("testdata/vidu_success.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/query/viduq3-turbo/vidu-task":
			if r.Header.Get("Authorization") != "Bearer sk-test" {
				t.Error("query lost supplier authentication")
			}
			_, _ = w.Write([]byte(strings.ReplaceAll(string(raw), "https://example.test/vidu.mp4", baseURL+"/vidu.mp4")))
		case "/vidu.mp4":
			_, _ = w.Write([]byte("vidu-video-bytes"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	baseURL = server.URL
	p := newTestProvider(server)
	job, err := p.GetVideo(context.Background(), models.ViduQ3Turbo, "vidu-task")
	if err != nil || job.State != provider.VideoJobSucceeded {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	var data []byte
	finals := 0
	err = p.ReadVideoResult(context.Background(), models.ViduQ3Turbo, "vidu-task", func(event *modelhubv2.GenerateEvent) error {
		for _, item := range event.GetItems() {
			data = append(data, item.GetVideo().GetData()...)
		}
		if event.GetFinal() {
			finals++
		}
		return nil
	})
	if err != nil || string(data) != "vidu-video-bytes" || finals != 1 {
		t.Fatalf("bytes=%q finals=%d err=%v", data, finals, err)
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
	if err := submitGetRead(context.Background(), p, model, testGenRequest("https://cdn.example/frame.png"), nil); err != nil {
		t.Fatal(err)
	}
}
