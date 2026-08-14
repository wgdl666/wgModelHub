package modelhub

import (
	"context"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordingText struct {
	model   string
	request *modelhubv2.GenerateRequest
}

func (r *recordingText) Generate(_ context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	r.model = model
	r.request = request
	return provider.TextFinalEvent("ok", nil, "resp", "stop", nil), nil
}

func (r *recordingText) GenerateStream(context.Context, string, *modelhubv2.GenerateRequest, provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	return provider.MetadataFinalEvent("", "", nil), nil
}

type recordingImage struct {
	model string
}

func (r *recordingImage) GenerateImage(_ context.Context, model string, _ *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	r.model = model
	// 路由测试只需合法 final：诊断文本即可，不必真有图片字节。
	return &modelhubv2.GenerateEvent{
		Final: true,
		Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "ok"}}},
	}, nil
}

type streamingText struct{}

func (streamingText) Generate(context.Context, string, *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	return nil, nil
}

func (streamingText) GenerateStream(_ context.Context, _ string, _ *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	_ = emit(provider.TextDeltaEvent("hello"))
	// 即使供应商误发 final，service 也必须收敛成唯一一次元数据 final。
	_ = emit(&modelhubv2.GenerateEvent{Final: true, ResponseId: "stale", Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "stale"}}}})
	return provider.MetadataFinalEvent("resp-1", "stop", nil), nil
}

type generateRecorder struct {
	grpc.ServerStream
	ctx    context.Context
	events []*modelhubv2.GenerateEvent
}

func (r *generateRecorder) Context() context.Context { return r.ctx }

func (r *generateRecorder) Send(event *modelhubv2.GenerateEvent) error {
	r.events = append(r.events, event)
	return nil
}

func textRequest(model, userText string) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{
		Model: model,
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: userText}}},
				}},
			}},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
}

func TestServiceRoutesTextByRealModel(t *testing.T) {
	text := &recordingText{}
	service := New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}})

	stream := &generateRecorder{ctx: context.Background()}
	if err := service.Generate(textRequest("chat-model", "do"), stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 1 || !stream.events[0].GetFinal() || len(stream.events[0].GetItems()) == 0 {
		t.Fatalf("events=%#v", stream.events)
	}
	if stream.events[0].GetItems()[0].GetText() != "ok" || text.model != "chat-model" {
		t.Fatalf("event=%#v model=%q", stream.events[0], text.model)
	}
	// 真实模型 ID 原样下发；任务文本留在 Input.items。
	if text.request.GetModel() != "chat-model" {
		t.Fatalf("request=%#v", text.request)
	}
}

func TestServiceRejectsCapabilityMismatch(t *testing.T) {
	service := New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ltx": {Models: []string{"ltx"}, LTX: &config.LTXProviderConfig{
				BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
			}},
		},
	}, map[string]provider.Set{"ltx": {Video: nil}})

	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(textRequest("ltx", "x"), stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
	st, _ := status.FromError(err)
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

func TestServiceRoutesImageByRealModel(t *testing.T) {
	image := &recordingImage{}
	service := New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini": {Models: []string{"artwork-model"}, Gemini: &config.GeminiProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"gemini": {Image: image}})

	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(&modelhubv2.GenerateRequest{
		Model:  "artwork-model",
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, stream)
	if err != nil {
		t.Fatal(err)
	}
	if image.model != "artwork-model" {
		t.Fatalf("model=%q", image.model)
	}
}

func TestGenerateStreamSendsExactlyOneMetadataFinal(t *testing.T) {
	service := New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini": {Models: []string{"chat-model"}, Gemini: &config.GeminiProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"gemini": {Text: streamingText{}}})
	stream := &generateRecorder{ctx: context.Background()}
	req := textRequest("chat-model", "hi")
	req.Output.Stream = true
	if err := service.Generate(req, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 2 {
		t.Fatalf("events=%#v", stream.events)
	}
	if stream.events[0].GetFinal() || stream.events[0].GetItems()[0].GetText() != "hello" {
		t.Fatalf("first=%#v", stream.events[0])
	}
	final := stream.events[1]
	if !final.GetFinal() || final.GetResponseId() != "resp-1" || len(final.GetItems()) != 0 {
		t.Fatalf("final=%#v", final)
	}
}

func TestGenerateImageRejectsOversizedInlineMedia(t *testing.T) {
	service := New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini": {Models: []string{"artwork-model"}, Gemini: &config.GeminiProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"gemini": {Image: &recordingImage{}}})

	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(&modelhubv2.GenerateRequest{
		Model: "artwork-model",
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{
					Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Data{Data: make([]byte, protocol.MaxMediaBytes+1)},
					}},
				}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestGenerateRejectsMissingOutputKind(t *testing.T) {
	service := New(config.Config{}, nil)
	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(&modelhubv2.GenerateRequest{Model: "chat-model"}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}
