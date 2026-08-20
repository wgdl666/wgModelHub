package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

const tinyPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82"

func imageRequest(prompt, ratio, size string, images ...*modelhubv2.Media) *modelhubv2.GenerateRequest {
	parts := []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: prompt}}}
	for _, image := range images {
		parts = append(parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: image}})
	}
	out := &modelhubv2.ImageOutput{}
	if ratio != "" {
		out.AspectRatio = &ratio
	}
	if size != "" {
		out.ImageSize = &size
	}
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: parts,
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: out}},
	}
}

func TestBuildImageRequestBodyMapsRatioAndSingleReference(t *testing.T) {
	body, err := buildImageRequestBody(models.GPTImage2, imageRequest("red apple", "3:4", "2K", &modelhubv2.Media{
		MimeType: "image/png",
		Source:   &modelhubv2.Media_Data{Data: []byte("ref")},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != models.GPTImage2 || body["prompt"] != "red apple" || body["n"] != 1 {
		t.Fatalf("body=%#v", body)
	}
	if _, ok := body["response_format"]; ok {
		t.Fatalf("gpt-image-2 rejects response_format: %#v", body)
	}
	if body["size"] != "1024x1536" {
		t.Fatalf("size=%#v", body["size"])
	}
	image, ok := body["image"].(string)
	if !ok || !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Fatalf("image=%#v", body["image"])
	}
}

func TestBuildImageRequestBodyPrefersPixelImageSize(t *testing.T) {
	body, err := buildImageRequestBody(models.GPTImage2, imageRequest("hi", "3:4", "1024x1024"))
	if err != nil {
		t.Fatal(err)
	}
	if body["size"] != "1024x1024" {
		t.Fatalf("size=%#v", body["size"])
	}
}

func TestBuildImageRequestBodyKeepsMultipleReferences(t *testing.T) {
	body, err := buildImageRequestBody(models.GPTImage2, imageRequest("edit", "", "",
		&modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("a")}},
		&modelhubv2.Media{MimeType: "image/jpeg", Source: &modelhubv2.Media_Uri{Uri: "https://cdn.example/b.jpg"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	refs, ok := body["image"].([]string)
	if !ok || len(refs) != 2 || refs[1] != "https://cdn.example/b.jpg" {
		t.Fatalf("image=%#v", body["image"])
	}
}

func TestBuildImageRequestBodyRejectsEmptyPrompt(t *testing.T) {
	_, err := buildImageRequestBody(models.GPTImage2, imageRequest("  ", "", ""))
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("err=%v", err)
	}
}

func TestImagesGenerationsURLAppendsV1(t *testing.T) {
	p := &Provider{baseURL: "https://api.ominilink.ai"}
	if got := p.imagesGenerationsURL(); got != "https://api.ominilink.ai/v1/images/generations" {
		t.Fatalf("url=%q", got)
	}
	p.baseURL = "https://api.ominilink.ai/v1"
	if got := p.imagesGenerationsURL(); got != "https://api.ominilink.ai/v1/images/generations" {
		t.Fatalf("url=%q", got)
	}
}

func TestGenerateImagePostsImagesAPIAndReturnsInlinePNG(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/images/generations" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("auth=%q", request.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{
				"b64_json":       base64.StdEncoding.EncodeToString([]byte(tinyPNG)),
				"revised_prompt": "a red apple",
			}},
			"usage": map[string]int{"prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10},
		})
	}))
	defer server.Close()

	p, err := New("ominilink_gpt_image", "secret", server.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	event, err := p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("red apple", "1:1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if captured["model"] != models.GPTImage2 || captured["size"] != "1024x1024" {
		t.Fatalf("captured=%#v", captured)
	}
	if len(event.GetItems()) != 2 {
		t.Fatalf("items=%#v", event.GetItems())
	}
	image := event.GetItems()[0].GetImage()
	if image == nil || image.GetMimeType() != "image/png" || string(image.GetData()) != tinyPNG {
		t.Fatalf("image=%#v", image)
	}
	if event.GetItems()[1].GetText() != "a red apple" {
		t.Fatalf("text=%#v", event.GetItems()[1])
	}
	if event.GetUsage().GetTotalTokens() != 10 {
		t.Fatalf("usage=%#v", event.GetUsage())
	}
}

func TestGenerateImageFetchesURLResult(t *testing.T) {
	var serverURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"data": []map[string]string{{"url": serverURL + "/asset.png"}},
		})
	})
	mux.HandleFunc("/asset.png", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(tinyPNG))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	serverURL = server.URL

	p, err := New("ominilink_gpt_image", "secret", server.URL+"/v1", false)
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()

	event, err := p.GenerateImage(context.Background(), models.GPTImage2, imageRequest("hi", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	image := event.GetItems()[0].GetImage()
	if image == nil || string(image.GetData()) != tinyPNG {
		t.Fatalf("image=%#v", image)
	}
}
