package genai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/genai"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const geminiContentsRequiredMsg = "Gemini requires at least one non-system content message"

// countingTransport 记录出站次数；非法输入必须在进 SDK 前失败，调用次数保持 0。
type countingTransport struct {
	calls   atomic.Int32
	handler http.Handler
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	if t.handler == nil {
		return nil, http.ErrHandlerTimeout
	}
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

func newTestGeminiProvider(t *testing.T, transport http.RoundTripper) *Provider {
	t.Helper()
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  "test-key-not-used-on-reject",
		Backend: genai.BackendGeminiAPI,
		HTTPClient: &http.Client{
			Transport: transport,
		},
	})
	if err != nil {
		t.Fatalf("create genai client: %v", err)
	}
	return &Provider{name: "gemini", client: client}
}

func systemOnlyRequest() *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_SYSTEM,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "rules only"}}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
}

func systemAndUserRequest() *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{
			{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_SYSTEM,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "rules"}}},
			}}},
			{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "hi"}}},
			}}},
		}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
}

func TestGenerateRejectsSystemOnlyWithoutOutbound(t *testing.T) {
	transport := &countingTransport{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("supplier must not be contacted for SYSTEM-only input")
	})}
	p := newTestGeminiProvider(t, transport)
	_, err := p.Generate(context.Background(), models.Gemini38Flash, systemOnlyRequest())
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument {
		t.Fatalf("err=%v kind=%s", err, provider.Kind(err))
	}
	if !strings.Contains(err.Error(), geminiContentsRequiredMsg) {
		t.Fatalf("message=%q", err.Error())
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("outbound calls=%d want 0", transport.calls.Load())
	}
}

func TestGenerateStreamRejectsSystemOnlyWithoutOutbound(t *testing.T) {
	transport := &countingTransport{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("supplier must not be contacted for SYSTEM-only stream input")
	})}
	p := newTestGeminiProvider(t, transport)
	_, err := p.GenerateStream(context.Background(), models.Gemini38Flash, systemOnlyRequest(), nil)
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument || !strings.Contains(err.Error(), geminiContentsRequiredMsg) {
		t.Fatalf("err=%v", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("outbound calls=%d want 0", transport.calls.Load())
	}
}

func TestGenerateRejectsEmptyAfterConversionWithoutOutbound(t *testing.T) {
	// 有 USER role 但文本为空：转换后 contents 仍空，必须按转换结果拒绝，不能只数 role。
	transport := &countingTransport{}
	p := newTestGeminiProvider(t, transport)
	req := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{
			{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_SYSTEM,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "rules"}}},
			}}},
			{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role:  modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: ""}}},
			}}},
		}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
	_, err := p.Generate(context.Background(), models.Gemini25Flash, req)
	if err == nil || provider.Kind(err) != provider.ErrorInvalidArgument || !strings.Contains(err.Error(), geminiContentsRequiredMsg) {
		t.Fatalf("err=%v", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("outbound calls=%d want 0", transport.calls.Load())
	}
}

func TestGenerateSystemAndUserSucceedsWithFakeTransport(t *testing.T) {
	transport := &countingTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"hi"`) {
			t.Fatalf("request body missing user text: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`))
	})}
	p := newTestGeminiProvider(t, transport)
	event, err := p.Generate(context.Background(), models.Gemini38Flash, systemAndUserRequest())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if transport.calls.Load() != 1 {
		t.Fatalf("outbound calls=%d want 1", transport.calls.Load())
	}
	if event == nil || len(event.GetItems()) == 0 || event.GetItems()[0].GetText() != "ok" {
		t.Fatalf("event=%#v", event)
	}
}

func TestRequireGeminiContentsAcceptsValidShapes(t *testing.T) {
	p := &Provider{}
	cases := []struct {
		name string
		req  *modelhubv2.GenerateRequest
	}{
		{
			name: "image_user",
			req: &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{
						{Content: &modelhubv2.ContentPart_Text{Text: "describe"}},
						{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
							MimeType: "image/png",
							Source:   &modelhubv2.Media_Data{Data: []byte("png")},
						}}},
					},
				}},
			}}}},
		},
		{
			name: "tool_output",
			req: &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_ToolOutput{ToolOutput: &modelhubv2.ToolOutput{
					ToolCallId: "c1",
					ToolName:   "lookup",
					Output:     `{"ok":true}`,
				}},
			}}}},
		},
		{
			name: "assistant_tool_call",
			req: &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_ASSISTANT,
					ToolCalls: []*modelhubv2.ToolCall{{
						Id:            "call-1",
						Name:          "lookup",
						ArgumentsJson: []byte(`{"q":"x"}`),
					}},
				}},
			}}}},
		},
		{
			name: "cached_prefix_with_user",
			req: &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{
				CachedContent: "cachedContents/session-1",
				Items: []*modelhubv2.InputItem{{
					Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
						Role:  modelhubv2.Role_ROLE_USER,
						Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "continue"}}},
					}},
				}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := p.buildContents(tc.req)
			if err := requireGeminiContents(contents); err != nil {
				t.Fatalf("requireGeminiContents: %v contents=%#v", err, contents)
			}
		})
	}
}

func TestGeminiContentsRequiredMapsToInvalidArgumentGRPC(t *testing.T) {
	// 归类必须来自真实 Generate 拒收路径，不能手工 New(ErrorInvalidArgument) 绕过校验。
	transport := &countingTransport{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("supplier must not be contacted before InvalidArgument mapping")
	})}
	p := newTestGeminiProvider(t, transport)
	_, genErr := p.Generate(context.Background(), models.Gemini38Flash, systemOnlyRequest())
	if genErr == nil || provider.Kind(genErr) != provider.ErrorInvalidArgument {
		t.Fatalf("generate err=%v kind=%s", genErr, provider.Kind(genErr))
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("outbound calls=%d want 0", transport.calls.Load())
	}
	err := provider.ToStatus(genErr)
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected grpc status")
	}
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code=%v", st.Code())
	}
	if st.Message() != geminiContentsRequiredMsg {
		t.Fatalf("message=%q", st.Message())
	}
	var reason string
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			reason = info.Reason
		}
	}
	if reason != string(provider.ErrorInvalidArgument) {
		t.Fatalf("reason=%q", reason)
	}
}
