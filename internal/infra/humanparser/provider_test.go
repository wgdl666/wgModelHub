package humanparser

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func parserRequest(image []byte) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
		Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{
				Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
					MimeType: "image/png",
					Source:   &modelhubv2.Media_Data{Data: image},
				}},
			}},
		}},
	}}}}
}

func TestHumanParseUsesG7PathAndRejectsBusinessFailure(t *testing.T) {
	image := []byte("person-crop")
	var gotPath, gotAuth, gotType string
	var gotImage string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Image string `json:"image"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		gotImage = payload.Image
		_, _ = w.Write([]byte(`{"success":false,"message":"image side 16 < min 32"}`))
	}))
	defer server.Close()

	client, err := New("human_parse_g7", server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(t.Context(), models.HumanParse, parserRequest(image))
	if err == nil {
		t.Fatal("success=false must fail even when HTTP is 200")
	}
	if gotPath != "/human_parser/predict" {
		t.Fatalf("path=%s", gotPath)
	}
	if gotAuth != "" {
		t.Fatal("G7 parser must not send basic auth when credentials are empty")
	}
	if gotType != "application/json" {
		t.Fatalf("content-type=%s", gotType)
	}
	if gotImage != base64.StdEncoding.EncodeToString(image) {
		t.Fatalf("image=%s", gotImage)
	}
}

func TestHumanParseReturnsUpstreamJSONOnSuccess(t *testing.T) {
	const body = `{"success":true,"pred_uint8_b64":"AQID","pred_shape":[1,3],"id2label":{"1":"top"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/human_parser/predict" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client, err := New("human_parse_g7", server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	event, err := client.Generate(t.Context(), models.HumanParse, parserRequest([]byte("ok")))
	if err != nil {
		t.Fatal(err)
	}
	if len(event.GetItems()) != 1 || event.GetItems()[0].GetText() != body {
		t.Fatalf("event=%v", event.GetItems())
	}
}

func TestLegacyHumanParserKeepsPredictPath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		user, pass, ok := r.BasicAuth()
		if !ok || user != "wgdl" || pass != "secret" {
			t.Fatalf("auth user=%s ok=%v", user, ok)
		}
		_, _ = w.Write([]byte(`{"success":true,"pred_uint8_b64":"AQ","pred_shape":[1,1],"id2label":{}}`))
	}))
	defer server.Close()
	client, err := New("human_parser_cn", server.URL, "wgdl", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(t.Context(), models.HumanParser, parserRequest([]byte("old"))); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/predict" {
		t.Fatalf("path=%s", gotPath)
	}
	if _, err := client.Generate(t.Context(), "human_parse_typo", parserRequest([]byte("old"))); err == nil {
		t.Fatal("unknown model must be rejected before the request")
	}
}
