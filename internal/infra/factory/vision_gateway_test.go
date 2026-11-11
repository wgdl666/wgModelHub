package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/models/") && r.Header.Get("Authorization") == "Bearer "+token {
			key := strings.TrimPrefix(r.URL.Path, "/v1/models/")
			kind := map[string]string{models.HumanParse: "human_parse", models.SegmentPersonBria: "segment_person", models.SegmentSubjectBria: "segment_subject", "parser-v2": "human_parse", "subject-v2": "segment_subject"}[key]
			_ = json.NewEncoder(w).Encode(map[string]string{"model_key": key, "protocol": kind})
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("vision request did not use the configured gateway bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Model-Key") == "" {
			t.Error("vision request lost registered model key")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/vision/human-parser/human_parser/predict":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["image"] != encoded {
				t.Error("parser lost its inline JSON image")
			}
			fmt.Fprint(w, `{"success":true,"pred_shape":[1,1]}`)
		case "/v1/vision/human-yolo/predict":
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error("YOLO lost its multipart file")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil || string(body) != image || r.FormValue("conf") != "0.35" || r.FormValue("iou") != "0.45" {
				t.Error("YOLO file or detection thresholds changed")
			}
			fmt.Fprint(w, `{"persons":[{"confidence":0.9}]}`)
		case "/v1/vision/segment-person/api/segment", "/v1/vision/segment-person/api/segment_subject":
			if r.FormValue("method") != "person-bria-rmbg" || r.FormValue("image") != encoded || r.FormValue("output") != "b64" {
				t.Error("segmenter lost the configured method or image")
			}
			if strings.HasSuffix(r.URL.Path, "/segment") && r.FormValue("preprocess") != "false" {
				t.Error("person segmenter lost the preprocess option")
			}
			fmt.Fprintf(w, `{"refined_png_b64":%q}`, base64.StdEncoding.EncodeToString([]byte(r.URL.Path)))
		case "/v1/vision/facesim/compare":
			if r.FormValue("image_a") != encoded || r.FormValue("image_b") != encoded {
				t.Error("facesim lost one of its inline images")
			}
			fmt.Fprint(w, `{"success":true,"similarity":0.7}`)
		case "/v1/vision/lens_distortion_correction/correct":
			var body struct {
				Base64 string `json:"base64"`
				Mirror *bool  `json:"mirror_lens"`
				Crop   bool   `json:"crop"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Base64 != encoded ||
				body.Mirror == nil || *body.Mirror || !body.Crop {
				t.Error("lens correction lost the image or explicit false option")
			}
			fmt.Fprintf(w, `{"content_type":"image/png","image_base64":%q}`, base64.StdEncoding.EncodeToString([]byte(r.URL.Path)))
		default:
			t.Errorf("unexpected gateway path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()

	const direct = "http://127.0.0.1:1"
	sets, err := Build(context.Background(), config.Config{Providers: map[string]config.ProviderConfig{
		"gateway": {VMindGateway: &config.VMindGatewayProviderConfig{BaseURL: gateway.URL, PollInterval: 1,
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
	face, err := sets["face"].Text.Generate(context.Background(), models.FaceSim, faceRequest)
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
			ctx := context.Background()
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
