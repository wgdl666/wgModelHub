package genai

import (
	"context"
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
	calls int
	last  *http.Request
}

func (t *vertexRecordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	t.last = req.Clone(req.Context())
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{
			"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`)),
		Request: req,
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
