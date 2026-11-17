package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/wgdl666/kangaroo/callmeta"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestVisionAdaptersSubmitUnifiedJSON(t *testing.T) {
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
		model := strings.TrimPrefix(r.URL.Path, "/proxy/v1/gateway/requests/")
		if _, err := uuid.Parse(r.Header.Get("Idempotency-Key")); err != nil {
			t.Error("missing UUID request identity")
		}
		if r.Header.Get("X-Model-Key") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("adapter emitted a legacy request")
		}
		if model == r.URL.Path || model == "" {
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
		switch model {
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
			path := "segment-result"
			if model == models.SegmentSubjectBria || model == "subject-v2" {
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
			fmt.Fprintf(w, `{"content_type":"image/png","image_base64":%q}`, base64.StdEncoding.EncodeToString([]byte("lens-result")))
		default:
			t.Errorf("unexpected model %s", model)
			w.WriteHeader(404)
		}
	}))
	defer gateway.Close()

	sets, err := Build(context.Background(), config.Config{Providers: map[string]config.ProviderConfig{
		"gateway": {VMindGateway: &config.VMindGatewayProviderConfig{BaseURL: gateway.URL + "/proxy", PollInterval: 1,
			ResultStorage: config.ObjectStorageConfig{Provider: "s3", Region: "us-east-2", Bucket: "fixture",
				AccessKeyID: "test-only", AccessKeySecret: "test-only"}}},
		"parser": {VisionGateway: "gateway", Models: []string{models.HumanParse},
			HumanParser: &config.HumanParserProviderConfig{}},
		"yolo":    {VisionGateway: "gateway", HumanYOLO: &config.HumanYOLOProviderConfig{}},
		"segment": {VisionGateway: "gateway", SegmentPerson: &config.SegmentPersonProviderConfig{Method: "person-bria-rmbg"}},
		"face":    {VisionGateway: "gateway", FaceSim: &config.FaceSimProviderConfig{}},
		"lens":    {VisionGateway: "gateway", LensCorrection: &config.LensCorrectionProviderConfig{}},
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
		{"segment", "subject-v2", "segment-result_subject"},
		{"lens", "lens-v2", "lens-result"},
		{"yolo", models.HumanYOLO, `"persons"`},
		{"segment", models.SegmentPersonBria, "segment-result"},
		{"segment", models.SegmentSubjectBria, "segment-result_subject"},
		{"lens", models.LensDistortionCorrection, "lens-result"},
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

// 输入映射由上面的成功用例覆盖；这里保留迁移前的结果校验和错误边界。
func TestGatewayVisionResultValidation(t *testing.T) {
	t.Setenv("MODEL_GATEWAY_API_TOKEN", "fixture-token")
	for _, tc := range []struct {
		name, adapter, body, want string
		status                    int
		failed                    bool
	}{
		{"parser success", "parser", `{"success":true,"labels":[1]}`, `"labels":[1]`, 200, false},
		{"parser business failure", "parser", `{"success":false,"message":"bad image"}`, "", 200, true},
		{"parser malformed", "parser", `invalid`, "", 200, true},
		{"negative face cosine", "face", `{"success":true,"similarity":-0.25}`, `"cosine_similarity":-0.25`, 200, false},
		{"missing face", "face", `{"success":false,"similarity":null}`, "", 200, true},
		{"missing face score", "face", `{"success":true}`, "", 200, true},
		{"face score range", "face", `{"success":true,"similarity":1.5}`, "", 200, true},
		{"invalid segment JSON", "segment", `invalid`, "", 200, true},
		{"invalid segment image", "segment", `{"refined_png_b64":"%%%"}`, "", 200, true},
		{"segment success", "segment", `{"refined_png_b64":"cG5n"}`, "png", 200, false},
		{"lens JPEG passthrough", "lens", `{"content_type":"image/jpeg","image_base64":"anBlZw==","mirror_lens":false}`, "jpeg", 200, false},
		{"lens invalid JSON", "lens", `invalid`, "", 200, true},
		{"lens empty image", "lens", `{"image_base64":""}`, "", 200, true},
		{"lens failure once", "lens", `private-input`, "", 503, true},
		{"segment failure once", "segment", `failed`, "", 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/gateway/requests/registered-model" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("not a Gateway request")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer gateway.Close()
			sets, err := Build(context.Background(), visionFixtureConfig(gateway.URL))
			if err != nil {
				t.Fatal(err)
			}
			request := visionFixtureRequest(1)
			if tc.adapter == "face" {
				request = visionFixtureRequest(2)
			}
			var event *modelhubv2.GenerateEvent
			if sets[tc.adapter].Text != nil {
				event, err = sets[tc.adapter].Text.Generate(t.Context(), "registered-model", request)
			} else {
				event, err = sets[tc.adapter].Image.GenerateImage(t.Context(), "registered-model", request)
			}
			if (err != nil) != tc.failed {
				t.Fatalf("event=%v err=%v", event, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("Gateway called %d times", calls.Load())
			}
			if tc.want != "" {
				item := event.GetItems()[0]
				if item.GetImage() != nil {
					if string(item.GetImage().GetData()) != tc.want {
						t.Fatalf("image=%q", item.GetImage().GetData())
					}
				} else if !strings.Contains(item.GetText(), tc.want) {
					t.Fatalf("text=%s", item.GetText())
				}
			}
		})
	}
}

func TestGatewayVisionInvalidInputDoesNotSend(t *testing.T) {
	t.Setenv("MODEL_GATEWAY_API_TOKEN", "fixture-token")
	var calls atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer gateway.Close()
	sets, err := Build(context.Background(), visionFixtureConfig(gateway.URL))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parser", "yolo", "face", "segment", "lens"} {
		t.Run(name, func(t *testing.T) {
			empty := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{}}
			var err error
			if sets[name].Text != nil {
				_, err = sets[name].Text.Generate(t.Context(), "registered-model", empty)
			} else {
				_, err = sets[name].Image.GenerateImage(t.Context(), "registered-model", empty)
			}
			if err == nil {
				t.Fatal("empty input accepted")
			}
		})
	}
	invalid := visionFixtureRequest(1)
	invalid.Input.ModelTraits = `{"mirror_lens":`
	if _, err := sets["lens"].Image.GenerateImage(t.Context(), "registered-model", invalid); err == nil {
		t.Fatal("invalid lens traits accepted")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid input sent %d requests", calls.Load())
	}
}

func visionFixtureRequest(count int) *modelhubv2.GenerateRequest {
	parts := make([]*modelhubv2.ContentPart, 0, count)
	for i := 0; i < count; i++ {
		parts = append(parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("input image")}}}})
	}
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: parts}}}}}}
}

func visionFixtureConfig(base string) config.Config {
	return config.Config{Providers: map[string]config.ProviderConfig{
		"gateway": {VMindGateway: &config.VMindGatewayProviderConfig{BaseURL: base, Token: "fixture-token", PollInterval: 1, ResultStorage: config.ObjectStorageConfig{Provider: "s3", Region: "us-east-2", Bucket: "fixture", AccessKeyID: "test-only", AccessKeySecret: "test-only"}}},
		"parser":  {VisionGateway: "gateway", HumanParser: &config.HumanParserProviderConfig{}},
		"yolo":    {VisionGateway: "gateway", HumanYOLO: &config.HumanYOLOProviderConfig{}},
		"face":    {VisionGateway: "gateway", FaceSim: &config.FaceSimProviderConfig{}},
		"segment": {VisionGateway: "gateway", SegmentPerson: &config.SegmentPersonProviderConfig{Method: "bria"}},
		"lens":    {VisionGateway: "gateway", LensCorrection: &config.LensCorrectionProviderConfig{}},
	}}
}
