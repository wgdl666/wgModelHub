package factory

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/wgdl666/kangaroo/callmeta"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestModelGatewaySynchronousRequests(t *testing.T) {
	const image = "image fixture"
	const token = "existing-gateway-api-token"
	t.Setenv("MODEL_GATEWAY_API_TOKEN", token)
	encoded := base64.StdEncoding.EncodeToString([]byte(image))
	var rejected atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		model, ok := strings.CutPrefix(r.URL.Path, "/v1/gateway/requests/")
		if !ok || r.Method != http.MethodPost {
			t.Errorf("unexpected gateway request %s %q", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Content-Type") != "application/json" {
			t.Error("gateway bearer or content type is wrong")
		}
		if r.Header.Get(callmeta.DeviceID) != "device-a" || r.Header.Get(callmeta.UserID) != "user-a" {
			t.Error("gateway lost caller identity")
		}
		if _, err := uuid.Parse(r.Header.Get("Idempotency-Key")); err != nil || r.Header.Get("X-Model-Key") != "" {
			t.Error("gateway must receive a UUID and take the model from the path")
		}
		var body struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Input == nil {
			t.Error("missing JSON input")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		input := body.Input
		w.Header().Set("Content-Type", "application/json")
		switch model {
		case models.HumanParse, "parser-v2":
			if input["image"] != encoded {
				t.Error("parser lost its inline image")
			}
			fmt.Fprint(w, `{"success":true,"pred_shape":[1,1]}`)
		case models.HumanYOLO:
			if input["file"] != encoded || input["conf"] != "0.35" || input["iou"] != "0.45" {
				t.Error("YOLO file or detection thresholds changed")
			}
			fmt.Fprint(w, `{"persons":[{"confidence":0.9}]}`)
		case models.SegmentPersonBria, models.SegmentSubjectBria:
			if input["image"] != encoded || input["output"] != "b64" || input["method"] != nil ||
				input["person_method"] != "person-birefnet-portrait" || input["subject_method"] != "person-bria-rmbg" {
				t.Error("segmenter lost its image or configured algorithms")
			}
			fmt.Fprintf(w, `{"refined_png_b64":%q}`, base64.StdEncoding.EncodeToString([]byte(model)))
		case models.FaceSim:
			if input["image_a"] != encoded || input["image_b"] != encoded {
				t.Error("facesim lost one of its inline images")
			}
			fmt.Fprint(w, `{"success":true,"similarity":0.7}`)
		case models.LensDistortionCorrection:
			if input["base64"] != encoded || input["mirror_lens"] != false || input["crop"] != true {
				t.Error("lens correction lost the image or explicit false")
			}
			fmt.Fprintf(w, `{"content_type":"image/png","image_base64":%q}`, encoded)
		case models.VMindDreamV1, models.VMindDecisionV1:
			messages, _ := json.Marshal(input["messages"])
			if input["model"] != nil || !strings.Contains(string(messages), "data:image/png;base64,"+encoded) {
				t.Error("chat must send its model in the URL and preserve message content")
			}
			if model == models.VMindDreamV1 {
				if input["stream"] != true {
					t.Error("chat lost its stream flag")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ready\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				return
			}
			traits, _ := input["model_traits"].(map[string]any)
			if traits["task"] != "fixture-task" {
				t.Error("decision lost its model traits")
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"decision"}}]}`)
		case "gateway-busy":
			rejected.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected model %q", model)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()

	const direct = "http://127.0.0.1:1"
	sets, err := Build(context.Background(), config.Config{Providers: map[string]config.ProviderConfig{
		"gateway": {VMindGateway: &config.VMindGatewayProviderConfig{BaseURL: gateway.URL, PollInterval: 1,
			ResultStorage: config.ObjectStorageConfig{Provider: "s3", Region: "us-east-2", Bucket: "fixture",
				AccessKeyID: "test-only", AccessKeySecret: "test-only"}}},
		"parser": {VisionGateway: "gateway", HumanParser: &config.HumanParserProviderConfig{}},
		"yolo":   {VisionGateway: "gateway", HumanYOLO: &config.HumanYOLOProviderConfig{BaseURL: direct}},
		"segment": {VisionGateway: "gateway", SegmentPerson: &config.SegmentPersonProviderConfig{
			BaseURL: direct, PersonMethod: "person-birefnet-portrait", SubjectMethod: "person-bria-rmbg"}},
		"face": {VisionGateway: "gateway", FaceSim: &config.FaceSimProviderConfig{BaseURL: direct, Username: "old", Password: "old"}},
		"lens": {VisionGateway: "gateway", LensCorrection: &config.LensCorrectionProviderConfig{BaseURL: direct}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	item := &modelhubv2.InputItem{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
		Role: modelhubv2.Role_ROLE_USER,
		Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
			MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte(image)},
		}}}},
	}}}
	ctx := callmeta.WithIdentity(context.Background(), "device-a", "user-a")
	for _, test := range []struct {
		instance, model, traits, want string
		items                         []*modelhubv2.InputItem
	}{
		{"parser", models.HumanParse, "", "\"success\":true", []*modelhubv2.InputItem{item}},
		{"parser", "parser-v2", "", "\"success\":true", []*modelhubv2.InputItem{item}},
		{"yolo", models.HumanYOLO, "", "\"persons\"", []*modelhubv2.InputItem{item}},
		{"face", models.FaceSim, "", "\"similarity\":0.7", []*modelhubv2.InputItem{item, item}},
		{"segment", models.SegmentPersonBria, "", models.SegmentPersonBria, []*modelhubv2.InputItem{item}},
		{"segment", models.SegmentSubjectBria, "", models.SegmentSubjectBria, []*modelhubv2.InputItem{item}},
		{"lens", models.LensDistortionCorrection, `{"mirror_lens":false,"crop":true}`, image, []*modelhubv2.InputItem{item}},
		{"gateway", models.VMindDecisionV1, `{"task":"fixture-task"}`, "decision", []*modelhubv2.InputItem{item}},
	} {
		t.Run(test.model, func(t *testing.T) {
			set := sets[test.instance]
			request := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: test.items, ModelTraits: test.traits}}
			if set.Text != nil {
				event, err := set.Text.Generate(ctx, test.model, request)
				if err != nil || len(event.GetItems()) != 1 || !strings.Contains(event.GetItems()[0].GetText(), test.want) {
					t.Fatalf("gateway text result: %v, %v", event, err)
				}
				return
			}
			event, err := set.Image.GenerateImage(ctx, test.model, request)
			if err != nil || len(event.GetItems()) == 0 || string(event.GetItems()[0].GetImage().GetData()) != test.want {
				t.Fatalf("gateway image result: %v, %v", event, err)
			}
		})
	}

	request := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{item}}}
	t.Run(models.VMindDreamV1, func(t *testing.T) {
		var text strings.Builder
		event, err := sets["gateway"].Text.GenerateStream(ctx, models.VMindDreamV1, request, func(event *modelhubv2.GenerateEvent) error {
			text.WriteString(event.GetItems()[0].GetText())
			return nil
		})
		if err != nil || !event.GetFinal() || text.String() != "ready" {
			t.Fatalf("gateway stream result: %v, %q, %v", event, text.String(), err)
		}
	})
	t.Run("no retry after gateway rejection", func(t *testing.T) {
		for _, instance := range []string{"segment", "lens"} {
			_, err := sets[instance].Image.GenerateImage(ctx, "gateway-busy", request)
			if err == nil {
				t.Errorf("%s accepted a rejected request", instance)
			}
		}
		if got := rejected.Load(); got != 2 {
			t.Errorf("gateway received %d requests, want one per adapter", got)
		}
	})
}
