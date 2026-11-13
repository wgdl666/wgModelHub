package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/wgdl666/kangaroo/callmeta"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestVisionGatewayPreservesAdapterProtocol(t *testing.T) {
	const image = "image fixture"
	const token = "existing-gateway-api-token"
	t.Setenv("MODEL_GATEWAY_API_TOKEN", token)
	encoded := base64.StdEncoding.EncodeToString([]byte(image))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.Header.Get(callmeta.DeviceID) != "device-a" || r.Header.Get(callmeta.UserID) != "user-a" {
			t.Error("vision gateway lost identity")
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("vision request did not use the configured gateway bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Model-Key") == "" {
			t.Error("vision request lost registered model key")
		}
		if r.URL.Path != "/proxy/v1/gateway/requests/"+r.Header.Get("X-Model-Key") || r.Header.Get("Idempotency-Key") == "" {
			t.Errorf("not a unified model submission: %s", r.URL.Path)
		}
		var envelope struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			return
		}
		var body map[string]any
		if err := json.Unmarshal(envelope.Input, &body); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("X-Model-Key") {
		case models.HumanParse, "parser-v2":
			if body["image"] != encoded {
				t.Error("parser lost image")
			}
			fmt.Fprint(w, `{"success":true,"pred_shape":[1,1]}`)
		case models.HumanYOLO, "detector-v2":
			if body["file"] != encoded || body["conf"] != "0.35" || body["iou"] != "0.45" {
				t.Errorf("YOLO input changed: %+v", body)
			}
			fmt.Fprint(w, `{"persons":[{"confidence":0.9}]}`)
		case models.SegmentPersonBria, models.SegmentSubjectBria, "subject-v2":
			if body["image"] != encoded || body["output"] != "b64" || body["person_method"] != "person-bria-rmbg" || body["subject_method"] != "person-bria-rmbg" {
				t.Errorf("segment input changed: %+v", body)
			}
			path := "/v1/vision/segment-person/api/segment"
			if model := r.Header.Get("X-Model-Key"); model == models.SegmentSubjectBria || model == "subject-v2" {
				path += "_subject"
			}
			fmt.Fprintf(w, `{"refined_png_b64":%q}`, base64.StdEncoding.EncodeToString([]byte(path)))
		case models.FaceSim:
			if body["image_a"] != encoded || body["image_b"] != encoded {
				t.Error("facesim lost images")
			}
			fmt.Fprint(w, `{"success":true,"similarity":0.7}`)
		case models.LensDistortionCorrection, "lens-v2":
			if body["base64"] != encoded || body["mirror_lens"] != false || body["crop"] != true {
				t.Errorf("lens input changed: %+v", body)
			}
			fmt.Fprintf(w, `{"content_type":"image/png","image_base64":%q}`, base64.StdEncoding.EncodeToString([]byte("/v1/vision/lens_distortion_correction/correct")))
		default:
			t.Errorf("unexpected model %s", r.Header.Get("X-Model-Key"))
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()

	const direct = "http://127.0.0.1:1"
	sets, err := Build(context.Background(), config.Config{Providers: map[string]config.ProviderConfig{
		"gateway": {VMindGateway: &config.VMindGatewayProviderConfig{BaseURL: gateway.URL + "/proxy", PollInterval: 1,
			ResultStorage: config.ObjectStorageConfig{Provider: "s3", Region: "us-east-2", Bucket: "fixture",
				AccessKeyID: "test-only", AccessKeySecret: "test-only"}}},
		"parser": {VisionGateway: "gateway", Models: []string{models.HumanParse},
			HumanParser: &config.HumanParserProviderConfig{BaseURL: direct}},
		"yolo": {VisionGateway: "gateway", HumanYOLO: &config.HumanYOLOProviderConfig{
			BaseURL: direct, Username: "old-user", Password: "old-password"}},
		"segment": {VisionGateway: "gateway", SegmentPerson: &config.SegmentPersonProviderConfig{BaseURL: direct, Method: "person-bria-rmbg"}},
		"face":    {VisionGateway: "gateway", FaceSim: &config.FaceSimProviderConfig{BaseURL: direct, Username: "old", Password: "old"}},
		"lens":    {VisionGateway: "gateway", LensCorrection: &config.LensCorrectionProviderConfig{BaseURL: direct}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{
		Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
				MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(image)},
			}}}},
		}}}},
		ModelTraits: `{"mirror_lens":false,"crop":true}`,
	}}
	// 人脸比对保留双图契约，同时走与其他视觉模型相同的网关鉴权。
	faceRequest := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{request.Input.Items[0], request.Input.Items[0]}}}
	face, err := sets["face"].Text.Generate(callmeta.WithIdentity(context.Background(), "device-a", "user-a"), models.FaceSim, faceRequest)
	if err != nil || !strings.Contains(face.GetItems()[0].GetText(), `"similarity":0.7`) {
		t.Fatalf("facesim gateway result: %v", err)
	}
	for _, test := range []struct{ instance, model, want string }{
		{"parser", models.HumanParse, `"success":true`},
		{"parser", "parser-v2", `"success":true`},
		{"yolo", "detector-v2", `"persons"`},
		{"segment", "subject-v2", "/v1/vision/segment-person/api/segment_subject"},
		{"lens", "lens-v2", "/v1/vision/lens_distortion_correction/correct"},
		{"yolo", models.HumanYOLO, `"persons"`},
		{"segment", models.SegmentPersonBria, "/v1/vision/segment-person/api/segment"},
		{"segment", models.SegmentSubjectBria, "/v1/vision/segment-person/api/segment_subject"},
		{"lens", models.LensDistortionCorrection, "/v1/vision/lens_distortion_correction/correct"},
	} {
		t.Run(test.model, func(t *testing.T) {
			set := sets[test.instance]
			ctx := callmeta.WithIdentity(context.Background(), "device-a", "user-a")
			if set.Text != nil {
				event, err := set.Text.Generate(ctx, test.model, request)
				if err != nil || len(event.GetItems()) != 1 || !strings.Contains(event.GetItems()[0].GetText(), test.want) {
					t.Fatalf("gateway text result: %v", err)
				}
				return
			}
			event, err := set.Image.GenerateImage(ctx, test.model, request)
			if err != nil || len(event.GetItems()) == 0 || string(event.GetItems()[0].GetImage().GetData()) != test.want {
				t.Fatalf("gateway image result: %v", err)
			}
		})
	}
}
