package genai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	genaisdk "google.golang.org/genai"
)

// 测试用假密钥，仅校验出站头；不得当作真实凭据或写入文档。
const geminiTestAPIKey = "gemini-test-key-not-real"

// Generate 同步完成后再断言，无需 atomics / body 缓冲。
type vertexRecordingTransport struct {
	calls    int
	last     *http.Request
	body     []byte
	status   int
	response string
}

func (t *vertexRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		t.body = body
		_ = req.Body.Close()
	}
	t.last = req.Clone(req.Context())
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	payload := t.response
	if payload == "" {
		payload = `{
			"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
		Request:    req,
	}, nil
}

func TestNewVertexAIRequiresProject(t *testing.T) {
	if _, err := NewVertexAI(context.Background(), "vertex", "", "", ""); err == nil {
		t.Fatal("expected ADC project error")
	}
	if _, err := NewVertexAI(context.Background(), "vertex", "  ", "", ""); err == nil {
		t.Fatal("expected ADC project error")
	}
}

func TestVertexAIGenerateImageSendsNanoBananaRequest(t *testing.T) {
	ref := onePixelPNG(t)
	out := onePixelPNG(t)
	p, transport := newVertexADCTestProvider(t, 3600)
	transport.response = `{
		"candidates":[{"content":{"role":"model","parts":[
			{"text":"caption"},
			{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(out) + `"}}
		]},"finishReason":"STOP"}]
	}`
	// project 在初始化写入。token 交换之外，这次生图只请求一次模型。

	ratio := "3:4"
	size := "2K"
	event, err := p.GenerateImage(context.Background(), models.GeminiNanoBanana21, &modelhubv2.GenerateRequest{
		Model: models.GeminiNanoBanana21,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Text{Text: "fuse these"}},
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Data{Data: ref},
					}}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{
			AspectRatio:   &ratio,
			ImageSize:     &size,
			ThinkingLevel: modelhubv2.ThinkingLevel_THINKING_LEVEL_LOW,
		}}},
	})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if transport.calls != 1 || transport.last == nil {
		t.Fatalf("calls=%d", transport.calls)
	}
	req := transport.last
	if req.URL.Host != "aiplatform.googleapis.com" {
		t.Fatalf("host=%q", req.URL.Host)
	}
	if strings.Contains(req.URL.Host, "generativelanguage.googleapis.com") {
		t.Fatal("image request must stay on Vertex, not Gemini Developer API")
	}
	path := req.URL.EscapedPath()
	if !strings.Contains(path, "/projects/test-project/locations/global/publishers/google/models/"+models.GeminiNanoBanana21+":generateContent") {
		t.Fatalf("path=%q", path)
	}
	if strings.Contains(path, "should-not-appear-in-path") || strings.Contains(path, "us-central1") {
		t.Fatalf("path=%q", path)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "" {
		t.Fatalf("x-goog-api-key=%q", got)
	}
	if auth := req.Header.Get("Authorization"); auth != "Bearer token-1" {
		t.Fatalf("Authorization=%q", auth)
	}
	var body map[string]any
	if err := json.Unmarshal(transport.body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	generation := objectField(t, body, "generationConfig")
	imageConfig := objectField(t, generation, "imageConfig")
	if imageConfig["imageSize"] != "2K" || imageConfig["aspectRatio"] != "3:4" {
		t.Fatalf("imageConfig=%#v", imageConfig)
	}
	thinking := objectField(t, generation, "thinkingConfig")
	if thinking["thinkingLevel"] != "LOW" {
		t.Fatalf("thinkingConfig=%#v", thinking)
	}
	sent := inlineImageBytes(t, body)
	if !bytes.Equal(sent, ref) {
		t.Fatalf("reference image len=%d", len(sent))
	}
	decodeOnePixelPNG(t, sent)
	if len(event.GetItems()) != 2 || event.GetItems()[0].GetText() != "caption" {
		t.Fatalf("items=%#v", event.GetItems())
	}
	got := event.GetItems()[1].GetImage()
	if got.GetMimeType() != "image/png" || !bytes.Equal(got.GetData(), out) {
		t.Fatalf("image mime=%q len=%d", got.GetMimeType(), len(got.GetData()))
	}
	decodeOnePixelPNG(t, got.GetData())
}

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 12, G: 34, B: 56, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeOnePixelPNG(t *testing.T, data []byte) {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	if cfg.Width != 1 || cfg.Height != 1 {
		t.Fatalf("png size=%dx%d", cfg.Width, cfg.Height)
	}
}

func objectField(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	raw, ok := parent[key]
	if !ok {
		t.Fatalf("missing %s in %#v", key, parent)
	}
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s type=%T", key, raw)
	}
	return value
}

func inlineImageBytes(t *testing.T, body map[string]any) []byte {
	t.Helper()
	contents, ok := body["contents"].([]any)
	if !ok {
		t.Fatalf("contents type=%T", body["contents"])
	}
	for _, item := range contents {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			fields, ok := part.(map[string]any)
			if !ok {
				continue
			}
			inline, ok := fields["inlineData"].(map[string]any)
			if !ok {
				continue
			}
			encoded, ok := inline["data"].(string)
			if !ok {
				t.Fatalf("inlineData.data type=%T", inline["data"])
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			return decoded
		}
	}
	t.Fatal("request has no inline image")
	return nil
}

func TestVertexFlashImageUsesGlobalPath(t *testing.T) {
	out := onePixelPNG(t)
	p, transport := newVertexADCTestProvider(t, 3600)
	transport.response = `{
		"candidates":[{"content":{"role":"model","parts":[
			{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(out) + `"}}
		]},"finishReason":"STOP"}]
	}`

	if _, err := p.GenerateImage(context.Background(), models.Gemini25FlashImage, vertexImageRequest("blazer")); err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if transport.calls != 1 || transport.last == nil {
		t.Fatalf("calls=%d", transport.calls)
	}
	path := transport.last.URL.EscapedPath()
	want := "/projects/test-project/locations/global/publishers/google/models/" + models.Gemini25FlashImage + ":generateContent"
	if !strings.Contains(path, want) {
		t.Fatalf("path=%q", path)
	}
	if strings.Contains(path, "us-central1") || strings.Contains(path, "asia-southeast1") || strings.Contains(path, "should-not-appear-in-path") {
		t.Fatalf("path=%q", path)
	}
}

func TestGeminiBackendDoesNotUseVertexGlobalPath(t *testing.T) {
	transport := &vertexRecordingTransport{response: `{
		"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]
	}`}
	client, err := genaisdk.NewClient(context.Background(), &genaisdk.ClientConfig{
		APIKey:     geminiTestAPIKey,
		Backend:    genaisdk.BackendGeminiAPI,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{name: "gemini", client: client}
	if _, err := p.GenerateImage(context.Background(), models.GeminiNanoBanana21, vertexImageRequest("square")); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 || transport.last == nil {
		t.Fatalf("calls=%d", transport.calls)
	}
	if transport.last.URL.Host != "generativelanguage.googleapis.com" {
		t.Fatalf("host=%q", transport.last.URL.Host)
	}
	path := transport.last.URL.EscapedPath()
	if strings.Contains(path, "/projects/") || strings.Contains(path, "locations/global") {
		t.Fatalf("path=%q", path)
	}
}

func vertexTextRequest() *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "ping"}}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
}

func vertexImageRequest(text string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Model: models.GeminiNanoBanana21,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: text}}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}
}
