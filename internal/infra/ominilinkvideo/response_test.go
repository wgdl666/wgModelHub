package ominilinkvideo

import (
	"fmt"
	"testing"

	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func TestNativeTaskResponses(t *testing.T) {
	for _, tc := range []struct {
		name, model, raw, url, code, message string
		state                                provider.VideoJobState
	}{
		{name: "seedance", model: models.DreaminaSeedance20, raw: `{"status":"succeeded","content":{"video_url":"https://example.test/seedance.mp4"}}`, url: "https://example.test/seedance.mp4", state: provider.VideoJobSucceeded},
		{name: "veo", model: models.Veo31Generate001, raw: `{"done":true,"response":{"videos":[{"gcsUri":"https://example.test/veo.mp4"}]}}`, url: "https://example.test/veo.mp4", state: provider.VideoJobSucceeded},
		{name: "veo-data-response", model: models.Veo31Generate001, raw: `{"data":{"status":"success","response":{"videos":[{"gcsUri":"https://example.test/veo.mp4"}]}}}`, url: "https://example.test/veo.mp4", state: provider.VideoJobSucceeded},
		{name: "veo-running", model: models.Veo31Generate001, raw: `{"done":false}`, state: provider.VideoJobRunning},
		{name: "kling", model: models.KlingV3, raw: `{"Response":{"Status":"DONE","ResultVideoUrl":"https://example.test/kling.mp4"}}`, url: "https://example.test/kling.mp4", state: provider.VideoJobSucceeded},
		{name: "vidu", model: models.ViduQ3Turbo, raw: `{"state":"success","creations":[{"url":"https://example.test/vidu.mp4","cover_url":"https://example.test/cover.jpg"}]}`, url: "https://example.test/vidu.mp4", state: provider.VideoJobSucceeded},
		{name: "seedance-failed", model: models.DreaminaSeedance20, raw: `{"status":"failed","error":{"code":"ContentPolicyViolation","message":"rejected content"}}`, code: "ContentPolicyViolation", message: "rejected content", state: provider.VideoJobFailed},
		{name: "vidu-failed", model: models.ViduQ3Turbo, raw: `{"state":"failed","err_code":"ContentBlocked","err_msg":"blocked content"}`, code: "ContentBlocked", message: "blocked content", state: provider.VideoJobFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTaskResponse(tc.model, []byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got.state != tc.state || got.videoURL != tc.url || got.errorCode != tc.code {
				t.Fatalf("result=%+v", got)
			}
			if tc.message != "" && got.errorMessage != tc.message {
				t.Fatalf("message=%q want=%q", got.errorMessage, tc.message)
			}
		})
	}
}

func TestLegacyTaskWrappers(t *testing.T) {
	for _, model := range []string{models.DreaminaSeedance20, models.Veo31Generate001, models.KlingV3, models.ViduQ3Turbo} {
		for _, wrapper := range []string{"data", "Response"} {
			t.Run(model+"/"+wrapper, func(t *testing.T) {
				raw := fmt.Sprintf(`{"%s":{"status":"completed","output":{"video_url":"/legacy.mp4"}}}`, wrapper)
				got, err := parseTaskResponse(model, []byte(raw))
				if err != nil || got.state != provider.VideoJobSucceeded || got.videoURL != defaultDownloadBaseURL+"/legacy.mp4" {
					t.Fatalf("result=%+v err=%v", got, err)
				}
			})
		}
	}
}

func TestProtocolSpecificVideoFieldsStayLocal(t *testing.T) {
	raw := []byte(`{"status":"success","creations":[{"url":"https://example.test/vidu.mp4"}]}`)
	for _, model := range []string{models.DreaminaSeedance20, models.Veo31Generate001, models.KlingV3} {
		got, err := parseTaskResponse(model, raw)
		if err != nil || got.videoURL != "" {
			t.Fatalf("model=%s read a Vidu-only field: result=%+v err=%v", model, got, err)
		}
	}
}
