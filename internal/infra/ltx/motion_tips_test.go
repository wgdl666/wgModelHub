package ltx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/models"
)

func TestSubmitVideoMotionTipsMultipart(t *testing.T) {
	for _, tc := range []struct {
		name, traits, want, flag string
	}{
		{"per outfit", `{"motion_tips":"  缓慢侧转 30°\n保持全身入画  ","other":"ignored"}`, "  缓慢侧转 30°\n保持全身入画  ", ""},
		{"legacy", "", "", ""},
		{"unrelated traits", `{"other":"ignored"}`, "", ""},
		{"blank", `{"motion_tips":"  "}`, "  ", ""},
		{"transparent", `{"motion_tips":"walk","transparent_background":true}`, "walk", "true"},
		{"opaque", `{"transparent_background":false}`, "", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/vton" {
					t.Errorf("path=%s", r.URL.Path)
				}
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					http.Error(w, "bad multipart", http.StatusBadRequest)
					return
				}
				defer r.MultipartForm.RemoveAll()
				if got := r.FormValue("transparent_background"); got != tc.flag || r.Form.Has("transparent_background") != (tc.flag != "") {
					t.Errorf("transparent_background=%q want %q", got, tc.flag)
				}
				if got := r.FormValue("motion_tips"); got != tc.want {
					t.Errorf("motion_tips=%q want %q", got, tc.want)
				}
				if tc.want == "" && len(r.MultipartForm.Value["motion_tips"]) != 0 {
					t.Error("absent motion tips must not send an empty form field")
				}
				if r.FormValue("prompt") != "fixed scene" || r.Form.Has("model") {
					t.Errorf("prompt/model changed: %v", r.MultipartForm.Value)
				}
				file, _, err := r.FormFile("image")
				if err != nil {
					t.Error(err)
					return
				}
				defer file.Close()
				data, _ := io.ReadAll(file)
				if string(data) != "first frame" {
					t.Errorf("image=%q", data)
				}
				_, _ = w.Write([]byte(`{"job_id":"motion-job"}`))
			}))
			defer server.Close()
			p, err := New("wgdl_video", server.URL, "", 4, 24, 42, 1)
			if err != nil {
				t.Fatal(err)
			}
			p.client = server.Client()
			id, err := p.SubmitVideo(context.Background(), models.VWorldOutfitVideo10, motionRequest(tc.traits))
			if err != nil || id != "motion-job" {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
}

func TestSubmitVideoRejectsInvalidMotionTraitsBeforeFetchingImage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not call upstream", http.StatusInternalServerError)
	}))
	defer server.Close()
	p, err := New("wgdl_video", server.URL, "", 4, 24, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	for _, traits := range []string{`broken`, `[]`, `null`, `{"motion_tips":42}`, `{"motion_tips":{}}`, `{"motion_tips":null}`, `{"transparent_background":null}`, `{"transparent_background":"false"}`, `{"transparent_background":1}`} {
		req := motionRequest(traits)
		req.Input.Items[0].GetMessage().Parts[0].GetImage().Source = &modelhubv2.Media_Uri{Uri: server.URL + "/frame.png"}
		if _, err := p.SubmitVideo(context.Background(), models.VWorldOutfitVideo10, req); err == nil {
			t.Errorf("accepted invalid traits %s", traits)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
}

func motionRequest(traits string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Model: models.VWorldOutfitVideo10,
		Input: &modelhubv2.Input{
			ModelTraits: traits,
			Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("first frame")}}}},
					{Content: &modelhubv2.ContentPart_Text{Text: "fixed scene"}},
				},
			}}}},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{Resolution: "720p"}}},
	}
}

func TestGatewayTransparentBackgroundIsOptionalBoolean(t *testing.T) {
	for _, tc := range []struct {
		name   string
		traits string
		want   json.RawMessage
	}{
		{"omitted", `{}`, nil},
		{"explicit false", `{"transparent_background":false}`, json.RawMessage("false")},
		{"explicit true", `{"motion_tips":"walk","transparent_background":true}`, json.RawMessage("true")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input map[string]json.RawMessage `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if got := string(body.Input["transparent_background"]); got != string(tc.want) {
					t.Errorf("transparent_background=%q want %q", got, tc.want)
				}
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]string{"request_id": r.Header.Get("Idempotency-Key"), "state": "queued"})
			}))
			defer server.Close()
			client, err := vmindgateway.New("test", server.URL, "test-token", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = NewGateway("test", client, 1).SubmitVideo(t.Context(), models.VWorldOutfitVideo10, motionRequest(tc.traits))
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
