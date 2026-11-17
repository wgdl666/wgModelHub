package factory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func TestMediaGatewayUsesRegisteredModelKeys(t *testing.T) {
	const imageModel, videoModel = "registered-tryon-v2", "registered-video-v2"
	const token = "test-token"
	imageData, videoData := []byte("\x89PNG\r\n\x1a\nimage-result"), []byte("video-result")
	t.Setenv("MODEL_GATEWAY_API_TOKEN", token)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/test-results/outputs/") {
			if strings.HasSuffix(r.URL.Path, ".png") {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(imageData)
			} else {
				w.Header().Set("Content-Type", "video/mp4")
				_, _ = w.Write(videoData)
			}
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("gateway credentials were lost")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		state, extension, code := "succeeded", "mp4", http.StatusOK
		if r.Method == http.MethodPost {
			var body struct {
				Output string `json:"output"`
				Input  struct {
					Text   string                  `json:"text"`
					Prompt string                  `json:"prompt"`
					Images []struct{ Data []byte } `json:"images"`
				} `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "invalid input", http.StatusBadRequest)
				return
			}
			count, output, prompt := 1, "video", body.Input.Prompt
			switch r.URL.Path {
			case "/v1/gateway/requests/" + imageModel:
				count, extension, output, prompt = 2, "png", "image", body.Input.Text
			case "/v1/gateway/requests/" + videoModel:
				state, code = "queued", http.StatusAccepted
			default:
				t.Errorf("model key changed before submission: %s", r.URL.Path)
			}
			if body.Output != output || prompt != "keep prompt" || len(body.Input.Images) != count || !bytes.Equal(body.Input.Images[0].Data, imageData) {
				t.Error("registered model lost its protocol input")
			}
		} else if r.Method != http.MethodGet || r.URL.Path != "/v1/gateway/requests" {
			t.Errorf("unexpected gateway request %s %s", r.Method, r.URL.Path)
		}
		id := r.Header.Get("Idempotency-Key")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"request_id": id, "state": state,
			"result": map[string]any{"files": []map[string]string{{
				"provider": "s3", "region": "us-east-2", "bucket": "test-results", "key": "outputs/" + id + "/0." + extension,
			}}},
		})
	}))
	defer upstream.Close()
	sets, err := Build(t.Context(), config.Config{Providers: map[string]config.ProviderConfig{"gateway": {
		Models: []string{imageModel, videoModel},
		VMindGateway: &config.VMindGatewayProviderConfig{BaseURL: upstream.URL, PollInterval: 1,
			ResultStorage: config.ObjectStorageConfig{Provider: "s3", Region: "us-east-2", Bucket: "test-results", Endpoint: upstream.URL,
				AccessKeyID: "test-key", AccessKeySecret: "test-secret"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	part := &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
		MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: imageData},
	}}}
	message := &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{
		{Content: &modelhubv2.ContentPart_Text{Text: "keep prompt"}}, part, part,
	}}
	request := &modelhubv2.GenerateRequest{Model: imageModel, Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
		Item: &modelhubv2.InputItem_Message{Message: message},
	}}}}
	// 新模型名直接提交；测试网关不提供预查询接口，仍须保留原始输入和结果读取。
	image, err := sets["gateway"].Image.GenerateImage(t.Context(), imageModel, request)
	if err != nil || len(image.GetItems()) != 1 || !bytes.Equal(image.Items[0].GetImage().GetData(), imageData) {
		t.Fatalf("registered image result: %v %v", image, err)
	}
	request.Model, message.Parts = videoModel, message.Parts[:2]
	video := sets["gateway"].Video
	id, err := video.SubmitVideo(t.Context(), videoModel, request)
	if err != nil {
		t.Fatal(err)
	}
	job, err := video.GetVideo(t.Context(), videoModel, id)
	if err != nil || job.State != provider.VideoJobSucceeded {
		t.Fatalf("registered video status: %+v %v", job, err)
	}
	var actual []byte
	err = video.ReadVideoResult(t.Context(), videoModel, id, func(event *modelhubv2.GenerateEvent) error {
		for _, item := range event.Items {
			actual = append(actual, item.GetVideo().GetData()...)
		}
		return nil
	})
	if err != nil || !bytes.Equal(actual, videoData) {
		t.Fatalf("registered video result: %q %v", actual, err)
	}
}
