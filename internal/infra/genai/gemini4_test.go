package genai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/prefixcache"
	"github.com/wgdl666/wgModelHub/models"
	genaisdk "google.golang.org/genai"
	"google.golang.org/protobuf/proto"
)

func TestGemini4Thinking(t *testing.T) {
	budget := int32(1024)
	for _, tc := range []struct {
		name string
		text *modelhubv2.TextOutput
		want genaisdk.ThinkingLevel
	}{
		{"default", &modelhubv2.TextOutput{}, ""},
		{"enabled", &modelhubv2.TextOutput{Thinking: modelhubv2.ThinkingMode_THINKING_MODE_ENABLED}, ""},
		{"budget_only", &modelhubv2.TextOutput{ThinkingBudget: &budget}, ""},
		{"disabled", &modelhubv2.TextOutput{Thinking: modelhubv2.ThinkingMode_THINKING_MODE_DISABLED, ThinkingLevel: modelhubv2.ThinkingLevel_THINKING_LEVEL_HIGH}, genaisdk.ThinkingLevelLow},
		{"minimal", &modelhubv2.TextOutput{ThinkingLevel: modelhubv2.ThinkingLevel_THINKING_LEVEL_MINIMAL}, genaisdk.ThinkingLevelLow},
		{"low", &modelhubv2.TextOutput{ThinkingLevel: modelhubv2.ThinkingLevel_THINKING_LEVEL_LOW}, genaisdk.ThinkingLevelLow},
		{"medium", &modelhubv2.TextOutput{ThinkingLevel: modelhubv2.ThinkingLevel_THINKING_LEVEL_MEDIUM}, genaisdk.ThinkingLevelMedium},
		{"high_with_budget", &modelhubv2.TextOutput{ThinkingLevel: modelhubv2.ThinkingLevel_THINKING_LEVEL_HIGH, ThinkingBudget: &budget}, genaisdk.ThinkingLevelHigh},
	} {
		for _, model := range []string{models.Gemini4Argon, models.Gemini4Carbon} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				cfg := (&Provider{}).buildConfig(model, &modelhubv2.GenerateRequest{
					Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: tc.text}},
				})
				if tc.want == "" {
					if cfg.ThinkingConfig != nil {
						t.Fatalf("must leave the upstream MEDIUM default: %#v", cfg.ThinkingConfig)
					}
				} else if cfg.ThinkingConfig == nil || cfg.ThinkingConfig.ThinkingLevel != tc.want || cfg.ThinkingConfig.ThinkingBudget != nil {
					t.Fatalf("thinking=%#v want level=%s without budget", cfg.ThinkingConfig, tc.want)
				}
			})
		}
	}
}

// 两个后端都使用现有 SDK；所有 HTTP（包括 ADC 换 token）均由本地 transport 返回。
func TestGemini4BackendsPreserveToolLoopAndBypassExplicitCache(t *testing.T) {
	for _, tc := range []struct {
		model   string
		backend string
	}{
		{models.Gemini4Argon, "developer"},
		{models.Gemini4Argon, "vertex"},
		{models.Gemini4Carbon, "developer"},
		{models.Gemini4Carbon, "vertex"},
	} {
		t.Run(tc.model+"/"+tc.backend, func(t *testing.T) {
			transport := &vertexRecordingTransport{}
			var p *Provider
			path := "/v1beta/models/" + tc.model
			if tc.backend == "vertex" {
				var adc *vertexADCTestTransport
				p, adc = newVertexADCTestProvider(t, 3600)
				transport = &adc.vertexRecordingTransport
				path = "/v1beta1/projects/test-project/locations/global/publishers/google/models/" + tc.model
			} else {
				p = newTestGeminiProvider(t, transport)
			}
			store := newPrefixMemoryStore()
			manager := prefixcache.New(context.Background(), store)
			t.Cleanup(manager.Close)
			p.ConfigurePrefixCache(manager, func(string) bool { return true })
			req := prefixRequest()
			req.Model = tc.model
			req.GetOutput().GetText().ThinkingLevel = modelhubv2.ThinkingLevel_THINKING_LEVEL_MEDIUM
			req.GetOutput().GetText().ResponseFormat = &modelhubv2.ResponseFormat{
				Type:       modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA,
				JsonSchema: []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
			}
			req.Input.Items[1].GetMessage().Parts = append(req.Input.Items[1].GetMessage().Parts, &modelhubv2.ContentPart{
				Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: onePixelPNG(t)}}},
			})
			// 即使已有同 key 显式缓存，也必须完整发送 system/tools，不能引用旧资源。
			key := p.prefixKey(context.Background(), req.Model, p.buildConfig(req.Model, req))
			store.entries[key] = &prefixcache.Entry{Key: key, Name: "cachedContents/unsupported", ExpiresAt: prefixExpiry(time.Now().Add(prefixcache.TTL))}
			before := proto.Clone(req)
			transport.response = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-argon","name":"closet_search","args":{"color":"black"}},"thoughtSignature":"c2ln"}]},"finishReason":"STOP"}],"responseId":"first"}`
			first, err := p.Generate(context.Background(), req.Model, req)
			if err != nil {
				t.Fatal(err)
			}
			if transport.last.URL.Path != path+":generateContent" || !proto.Equal(req, before) {
				t.Fatal("wrong model endpoint or mutated request")
			}
			if len(first.Items) != 1 || first.Items[0].GetToolCall().GetId() != "call-argon" || string(first.Items[0].GetToolCall().GetThoughtSignature()) != "sig" {
				t.Fatalf("tool identity/signature lost: %v", first)
			}
			req.Input.Items = append(req.Input.Items,
				&modelhubv2.InputItem{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_ASSISTANT, ToolCalls: []*modelhubv2.ToolCall{first.Items[0].GetToolCall()}}}},
				&modelhubv2.InputItem{Item: &modelhubv2.InputItem_ToolOutput{ToolOutput: &modelhubv2.ToolOutput{ToolCallId: "call-argon", ToolName: "closet_search", Output: `{"found":true}`}}},
			)
			req.Output.Stream = true
			before = proto.Clone(req)
			transport.response = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"{\\\"ok\\\":true}\"}]},\"finishReason\":\"STOP\"}],\"responseId\":\"second\",\"usageMetadata\":{\"promptTokenCount\":100,\"cachedContentTokenCount\":40,\"candidatesTokenCount\":5,\"thoughtsTokenCount\":2,\"totalTokenCount\":107}}\n\n"
			var text strings.Builder
			final, err := p.GenerateStream(context.Background(), req.Model, req, func(event *modelhubv2.GenerateEvent) error {
				for _, item := range event.Items {
					text.WriteString(item.GetText())
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			manager.Close()
			if transport.calls != 2 || transport.last.URL.Path != path+":streamGenerateContent" || !proto.Equal(req, before) {
				t.Fatal("unexpected cache request, wrong stream endpoint, or mutated request")
			}
			if text.String() != `{"ok":true}` || !final.Final || len(final.Items) != 0 || final.ResponseId != "second" || final.FinishReason != "STOP" {
				t.Fatalf("text=%q final=%v", text.String(), final)
			}
			if usage := final.GetUsage(); usage.GetCachedTokens() != 40 || usage.GetReasoningTokens() != 2 || usage.GetTotalTokens() != 107 {
				t.Fatalf("usage lost: %v", usage)
			}
			var body struct {
				Contents []*genaisdk.Content            `json:"contents"`
				System   *genaisdk.Content              `json:"systemInstruction"`
				Tools    []*genaisdk.Tool               `json:"tools"`
				Cache    string                         `json:"cachedContent"`
				Config   genaisdk.GenerateContentConfig `json:"generationConfig"`
			}
			if err := json.Unmarshal(transport.body, &body); err != nil {
				t.Fatal(err)
			}
			if body.Cache != "" || body.System == nil || len(body.Tools) != 1 || len(body.Contents) != 3 {
				t.Fatalf("full prefix or tool history lost: %s", transport.body)
			}
			call := body.Contents[1].Parts[0]
			response := body.Contents[2].Parts[0].FunctionResponse
			if call.FunctionCall.ID != "call-argon" || string(call.ThoughtSignature) != "sig" || response.ID != call.FunctionCall.ID || response.Name != call.FunctionCall.Name {
				t.Fatalf("tool loop identity lost: %s", transport.body)
			}
			if body.Config.ThinkingConfig.ThinkingLevel != genaisdk.ThinkingLevelMedium || body.Config.ThinkingConfig.ThinkingBudget != nil || body.Config.Temperature == nil || *body.Config.Temperature != 0 || body.Config.ResponseMIMEType != "application/json" || body.Config.ResponseJsonSchema == nil {
				t.Fatalf("output settings lost: %s", transport.body)
			}
			if image := body.Contents[0].Parts[1].InlineData; image == nil || image.MIMEType != "image/png" || len(image.Data) == 0 {
				t.Fatal("multimodal input lost")
			}
		})
	}
}
