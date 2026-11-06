package ltx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestSubmitVideoMotionTipsJSON(t *testing.T) {
	for _, tc := range []struct {
		name, traits, want string
	}{
		{"per outfit", `{"motion_tips":"  缓慢侧转 30°\n保持全身入画  ","other":"ignored"}`, "  缓慢侧转 30°\n保持全身入画  "},
		{"legacy", "", ""},
		{"unrelated traits", `{"other":"ignored"}`, ""},
		{"blank", `{"motion_tips":"  "}`, "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var submittedKey string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/gateway/requests/"+models.VWorldOutfitVideo10 {
					t.Errorf("path=%s", r.URL.Path)
				}
				payload, images := decodeGatewayRequest(t, r)
				if got := payload.Input["motion_tips"]; tc.want != "" && got != tc.want {
					t.Errorf("motion_tips=%v want %q", got, tc.want)
				}
				if _, present := payload.Input["motion_tips"]; tc.want == "" && present {
					t.Error("absent motion tips must be omitted")
				}
				if payload.Input["prompt"] != "fixed scene" || len(payload.Input) > 2 {
					t.Errorf("unexpected parameters: %v", payload.Input)
				}
				if len(images) != 1 || string(images[0].Data) != "first frame" {
					t.Errorf("first frame changed: %v", images)
				}
				submittedKey = acceptGatewayRequest(t, w, r)
			}))
			defer server.Close()
			p, err := New("wgdl_video", server.URL, "token", 4, 24, 42, 1)
			if err != nil {
				t.Fatal(err)
			}
			p.client = server.Client()
			id, err := p.SubmitVideo(context.Background(), models.VWorldOutfitVideo10, motionRequest(tc.traits))
			if err != nil || id != submittedKey {
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
	p, err := New("wgdl_video", server.URL, "token", 4, 24, 42, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	for _, traits := range []string{`broken`, `[]`, `null`, `{"motion_tips":42}`, `{"motion_tips":{}}`, `{"motion_tips":null}`} {
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
