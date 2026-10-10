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
)

// 测试用假密钥，仅校验出站头；不得当作真实凭据或写入文档。
const vertexTestAPIKey = "vertex-express-test-key-not-real"

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

func TestNewVertexAIRequiresAPIKey(t *testing.T) {
	if _, err := NewVertexAI(context.Background(), "vertex", ""); err == nil {
		t.Fatal("expected empty api_key error")
	}
	if _, err := NewVertexAI(context.Background(), "vertex", "  "); err == nil {
		t.Fatal("expected blank api_key error")
	}
}

func TestNewVertexAIBuildsWithoutADC(t *testing.T) {
	// 清掉 ADC 相关环境，证明 Express API key 路径不依赖应用默认凭据。
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	t.Setenv("GOOGLE_CLOUD_REGION", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	p, err := NewVertexAI(context.Background(), "vertex", vertexTestAPIKey)
	if err != nil {
		t.Fatalf("NewVertexAI without ADC: %v", err)
	}
	if p == nil || p.client == nil {
		t.Fatal("expected non-nil Vertex client")
	}
}

func TestVertexAIGenerateUsesExpressAPIKeyPath(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "should-not-appear-in-path")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	transport := &vertexRecordingTransport{}
	p, err := newVertexAI(context.Background(), "vertex", vertexTestAPIKey, &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("newVertexAI: %v", err)
	}

	event, err := p.Generate(context.Background(), models.Gemini25Flash, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "ping"}}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if transport.calls != 1 {
		t.Fatalf("outbound calls=%d want 1", transport.calls)
	}
	req := transport.last
	if req == nil {
		t.Fatal("missing outbound request")
	}
	if req.URL.Host != "aiplatform.googleapis.com" {
		t.Fatalf("host=%q want aiplatform.googleapis.com", req.URL.Host)
	}
	path := req.URL.EscapedPath()
	if !strings.Contains(path, "/publishers/google/models/"+models.Gemini25Flash+":generateContent") {
		t.Fatalf("path=%q want Vertex Express publishers path", path)
	}
	if strings.Contains(path, "/projects/") || strings.Contains(path, "should-not-appear-in-path") {
		t.Fatalf("Express path must omit project prefix: %q", path)
	}
	if strings.Contains(req.URL.Host, "generativelanguage.googleapis.com") {
		t.Fatal("must stay on Vertex backend, not Gemini Developer API")
	}
	if got := req.Header.Get("x-goog-api-key"); got != vertexTestAPIKey {
		t.Fatalf("x-goog-api-key=%q", got)
	}
	if auth := req.Header.Get("Authorization"); auth != "" {
		t.Fatalf("Authorization must be absent for API key mode, got %q", auth)
	}
	if event == nil || len(event.GetItems()) == 0 || event.GetItems()[0].GetText() != "ok" {
		t.Fatalf("event=%#v", event)
	}
}

func TestVertexAIGenerateImageSendsNanoBananaRequest(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "should-not-appear-in-path")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	ref := onePixelPNG(t)
	out := onePixelPNG(t)
	transport := &vertexRecordingTransport{response: `{
		"candidates":[{"content":{"role":"model","parts":[
			{"text":"caption"},
			{"inlineData":{"mimeType":"image/png","data":"` + base64.StdEncoding.EncodeToString(out) + `"}}
		]},"finishReason":"STOP"}]
	}`}
	p, err := newVertexAI(context.Background(), "vertex_nano_banana", vertexTestAPIKey, &http.Client{Transport: transport})
	if err != nil {
		t.Fatalf("newVertexAI: %v", err)
	}
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
	if !strings.Contains(path, "/publishers/google/models/"+models.GeminiNanoBanana21+":generateContent") {
		t.Fatalf("path=%q", path)
	}
	if strings.Contains(path, "/projects/") || strings.Contains(path, "should-not-appear-in-path") {
		t.Fatalf("Express path must omit project prefix: %q", path)
	}
	if got := req.Header.Get("x-goog-api-key"); got != vertexTestAPIKey {
		t.Fatalf("x-goog-api-key=%q", got)
	}
	if auth := req.Header.Get("Authorization"); auth != "" {
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
