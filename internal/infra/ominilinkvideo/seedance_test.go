package ominilinkvideo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/protobuf/proto"
)

func TestGenerateSeedancePayload(t *testing.T) {
	const model = models.DreaminaSeedance20
	const taskID = "seed-task"
	var createPayload map[string]any
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/bytedance/api/v3/contents/generations/tasks":
			_ = json.NewDecoder(r.Body).Decode(&createPayload)
			_, _ = w.Write([]byte(`{"id":"` + taskID + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v3/bytedance/api/v3/contents/generations/tasks/"+taskID:
			_, _ = w.Write([]byte(`{"status":"succeeded","content":{"video_url":"` + baseURL + `/vod/out.mp4"}}`))
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
	if err := submitGetRead(context.Background(), p, model, testGenRequest("https://cdn.example/frame.png"), func(ev *modelhubv2.GenerateEvent) error {
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
	if createPayload["ratio"] == nil || createPayload["model"] != model {
		t.Fatalf("payload=%v", createPayload)
	}
}

func TestSubmitSeedanceInlineBytesAsDataURI(t *testing.T) {
	const png = "fake-png"
	const model = models.DreaminaSeedance20
	var createPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method %s path %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&createPayload)
		_, _ = w.Write([]byte(`{"id":"seed-inline"}`))
	}))
	defer server.Close()
	p := newTestProvider(server)
	id, err := p.SubmitVideo(context.Background(), model, testInlineImageRequest([]byte(png), "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "seed-inline" {
		t.Fatalf("id=%s", id)
	}
	content, _ := createPayload["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content=%v", createPayload["content"])
	}
	imageBlock, _ := content[1].(map[string]any)
	imageURL, _ := imageBlock["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("url=%q", url)
	}
}

// 三张身份图必须按输入顺序完整传递；回归点是原实现静默丢掉第二、三张。
func TestSeedanceMultipleIdentityReferences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/bytedance/api/v3/contents/generations/tasks" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Content []struct {
				Type  string `json:"type"`
				Role  string `json:"role"`
				Image struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Content) != 4 {
			t.Fatalf("content count %d", len(payload.Content))
		}
		for i, name := range []string{"front", "side", "back"} {
			item := payload.Content[i+1]
			if item.Role != "reference_image" || item.Image.URL != "https://cdn.example/"+name+".png" {
				t.Fatalf("reference %d: %+v", i, item)
			}
		}
		_, _ = w.Write([]byte(`{"id":"three-view-task"}`))
	}))
	defer server.Close()
	request := testGenRequest("https://cdn.example/front.png")
	message := request.Input.Items[0].GetMessage()
	for _, name := range []string{"side", "back"} {
		message.Parts = append(message.Parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Uri{Uri: "https://cdn.example/" + name + ".png"}}}})
	}
	p := newTestProvider(server)
	if id, err := p.SubmitVideo(context.Background(), models.DreaminaSeedance20, request); err != nil || id != "three-view-task" {
		t.Fatalf("submit %s %v", id, err)
	}
}

func TestSeedanceInlineReferencesPreserveOrderAndMode(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v3/bytedance/api/v3/contents/generations/tasks" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var payload struct {
					Content []struct {
						Type  string `json:"type"`
						Text  string `json:"text"`
						Role  string `json:"role"`
						Image struct {
							URL string `json:"url"`
						} `json:"image_url"`
					} `json:"content"`
					GenerateAudio *bool `json:"generate_audio"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if len(payload.Content) != count+1 || payload.Content[0].Text != "walk" {
					t.Errorf("wrong content: %+v", payload.Content)
					return
				}
				role := "first_frame"
				if count > 1 {
					role = "reference_image"
				}
				for i, item := range payload.Content[1:] {
					want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{byte(i), 255, 0})
					if item.Role != role || item.Image.URL != want {
						t.Errorf("image %d: role=%s url=%s", i, item.Role, item.Image.URL)
					}
				}
				if payload.GenerateAudio == nil || *payload.GenerateAudio != (count == 1) {
					t.Error("single/multiple image audio behavior changed")
				}
				_, _ = w.Write([]byte(`{"id":"inline-references"}`))
			}))
			defer server.Close()
			req := testInlineImageRequest([]byte{0, 255, 0}, "image/png")
			message := req.Input.Items[0].GetMessage()
			for i := 1; i < count; i++ {
				message.Parts = append(message.Parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte{byte(i), 255, 0}}}}})
			}
			before := proto.Clone(req)
			if _, err := newTestProvider(server).SubmitVideo(context.Background(), models.DreaminaSeedance20, req); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !proto.Equal(req, before) {
				t.Fatal("submission count or original request changed")
			}
		})
	}
}

// 供应商失败终态必须终止业务轮询，保留原因；否则 PawPal 会一直等待直到超时。
func TestSeedanceV3FailedTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/bytedance/api/v3/contents/generations/tasks/failed-task" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"failed-task","status":"failed","error":{"code":"ContentPolicyViolation","message":"rejected content"}}`))
	}))
	defer server.Close()
	job, err := newTestProvider(server).GetVideo(context.Background(), models.DreaminaSeedance20Fast, "failed-task")
	if err != nil || job.State != provider.VideoJobFailed || job.Err == nil || !strings.Contains(job.Err.Error(), "ContentPolicyViolation") {
		t.Fatalf("job=%+v err=%v", job, err)
	}
}

func TestSeedanceVersionedModelResolution(t *testing.T) {
	for _, model := range []string{models.DreaminaSeedance20Mini, models.DreaminaSeedance20Fast} {
		resolution, _, _ := NormalizeParams(model, "1080p", 5, "1:1")
		if resolution != "720p" {
			t.Fatalf("%s resolution=%s", model, resolution)
		}
	}
}
