package modelhub

import (
	"context"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/internal/taskstore"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

// newTestService 为单测构造带 LiveConfig 的 Service，与生产 Listen 路径一致。
func newTestService(cfg config.Config, providers map[string]provider.Set, tasks taskstore.Store) *Service {
	return New(config.NewLiveConfig(cfg), providers, tasks)
}

func TestServiceHotReloadModelsEnterResolveAndListModels(t *testing.T) {
	image := &recordingImage{}
	cfg := config.Config{
		Logfire:  config.LogfireConfig{Token: "t", Env: "test", Service: "wg-model-hub"},
		Database: config.DatabaseConfig{DSN: "postgres://modelhub:modelhub@127.0.0.1:5432/modelhub?sslmode=disable"},
		Providers: map[string]config.ProviderConfig{
			"async_gpt_image": {
				Models: []string{models.GPTImage2, models.GPTImage25Flare},
				OpenAI: &config.OpenAIProviderConfig{APIKey: "k", BaseURL: "https://api.example/v1"},
			},
		},
	}
	live := config.NewLiveConfig(cfg)
	service := New(live, map[string]provider.Set{"async_gpt_image": {Image: image}}, nil)

	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(&modelhubv2.GenerateRequest{
		Model:  models.GPTImage25Sunburst,
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, stream)
	if err == nil {
		t.Fatal("sunburst must be unknown before hot reload")
	}

	listBefore, err := service.ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_IMAGE_GENERATION,
	})
	if err != nil {
		t.Fatal(err)
	}
	if contains(listModelIDs(listBefore), models.GPTImage25Sunburst) {
		t.Fatal("ListModels must not include sunburst before hot reload")
	}

	next := cfg
	next.Providers = map[string]config.ProviderConfig{
		"async_gpt_image": {
			Models: []string{models.GPTImage2, models.GPTImage25Flare, models.GPTImage25Sunburst},
			OpenAI: &config.OpenAIProviderConfig{APIKey: "k", BaseURL: "https://api.example/v1"},
		},
	}
	body, err := yaml.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	live.ApplyYAML(string(body))

	if live.Load().ModelRoutes()[models.GPTImage25Sunburst] != "async_gpt_image" {
		t.Fatalf("routes=%v", live.Load().ModelRoutes())
	}
	listAfter, err := service.ListModels(context.Background(), &modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_IMAGE_GENERATION,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(listModelIDs(listAfter), models.GPTImage25Sunburst) {
		t.Fatalf("ListModels after hot reload=%v", listModelIDs(listAfter))
	}

	stream = &generateRecorder{ctx: context.Background()}
	if err := service.Generate(&modelhubv2.GenerateRequest{
		Model:  models.GPTImage25Sunburst,
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, stream); err != nil {
		t.Fatal(err)
	}
	if image.model != models.GPTImage25Sunburst {
		t.Fatalf("model=%q", image.model)
	}

	// 删除后 resolve / ListModels 必须同步失效，且仍复用同一 provider client。
	removed := next
	removed.Providers = map[string]config.ProviderConfig{
		"async_gpt_image": {
			Models: []string{models.GPTImage2, models.GPTImage25Flare},
			OpenAI: &config.OpenAIProviderConfig{APIKey: "k", BaseURL: "https://api.example/v1"},
		},
	}
	body, err = yaml.Marshal(removed)
	if err != nil {
		t.Fatal(err)
	}
	live.ApplyYAML(string(body))
	if err := service.Generate(&modelhubv2.GenerateRequest{
		Model:  models.GPTImage25Sunburst,
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, &generateRecorder{ctx: context.Background()}); err == nil {
		t.Fatal("sunburst must be unknown after delete hot reload")
	}
}

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
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)

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
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ltx": {Models: []string{models.LTX}, LTX: &config.LTXProviderConfig{
				BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
			}},
		},
	}, map[string]provider.Set{"ltx": {Video: nil}}, nil)

	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(textRequest(models.LTX, "x"), stream)
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
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {Models: []string{models.GPTImage2}, OpenAI: &config.OpenAIProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"openai": {Image: image}}, nil)

	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(&modelhubv2.GenerateRequest{
		Model:  models.GPTImage2,
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, stream)
	if err != nil {
		t.Fatal(err)
	}
	if image.model != models.GPTImage2 {
		t.Fatalf("model=%q", image.model)
	}
}

func TestGenerateStreamSendsExactlyOneMetadataFinal(t *testing.T) {
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini": {Models: []string{"chat-model"}, Gemini: &config.GeminiProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"gemini": {Text: streamingText{}}}, nil)
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
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini": {Models: []string{"artwork-model"}, Gemini: &config.GeminiProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"gemini": {Image: &recordingImage{}}}, nil)

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
	service := newTestService(config.Config{}, nil, nil)
	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(&modelhubv2.GenerateRequest{Model: "chat-model"}, stream)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestValidateMediaAcceptsURIWithoutMIME(t *testing.T) {
	err := validateMedia(&modelhubv2.Media{
		Source: &modelhubv2.Media_Uri{Uri: "https://cdn.example.com/v1/assets/proxy?key=foo"},
	}, provider.ErrorInvalidArgument)
	if err != nil {
		t.Fatalf("validateMedia: %v", err)
	}
}

func TestValidateMediaRejectsInlineDataWithoutMIME(t *testing.T) {
	err := validateMedia(&modelhubv2.Media{
		Source: &modelhubv2.Media_Data{Data: []byte("x")},
	}, provider.ErrorInvalidArgument)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGenerateImageAcceptsURIWithoutMIMEAtService(t *testing.T) {
	image := &recordingImage{}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {Models: []string{models.GPTImage2}, OpenAI: &config.OpenAIProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"openai": {Image: image}}, nil)

	stream := &generateRecorder{ctx: context.Background()}
	err := service.Generate(&modelhubv2.GenerateRequest{
		Model: models.GPTImage2,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Text{Text: "hi"}},
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						Source: &modelhubv2.Media_Uri{Uri: "https://cdn.example.com/v1/assets/proxy?key=foo"},
					}}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}, stream)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if image.model != models.GPTImage2 {
		t.Fatalf("model=%q", image.model)
	}
}

func TestValidateGenerateRequestAllowsTextOnlyVideo(t *testing.T) {
	err := validateGenerateRequest(&modelhubv2.GenerateRequest{
		Model: models.DoubaoSeedance25,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "a cat walks"}}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{}}},
	}, config.CapabilityVideo)
	if err != nil {
		t.Fatalf("T2V should pass service validation: %v", err)
	}
}

func TestValidateGenerateRequestRejectsEmptyVideo(t *testing.T) {
	err := validateGenerateRequest(&modelhubv2.GenerateRequest{
		Model:  models.DoubaoSeedance25,
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{}}},
	}, config.CapabilityVideo)
	if err == nil {
		t.Fatal("expected empty video request to fail")
	}
}

type recordingSpeech struct {
	model   string
	request *modelhubv2.SynthesizeSpeechRequest
	resp    *modelhubv2.SynthesizeSpeechResponse
	err     error
}

func (r *recordingSpeech) SynthesizeSpeech(_ context.Context, model string, request *modelhubv2.SynthesizeSpeechRequest) (*modelhubv2.SynthesizeSpeechResponse, error) {
	r.model = model
	r.request = request
	if r.err != nil {
		return nil, r.err
	}
	if r.resp != nil {
		return r.resp, nil
	}
	return &modelhubv2.SynthesizeSpeechResponse{
		Audio: &modelhubv2.Media{
			MimeType: "audio/mpeg",
			Source:   &modelhubv2.Media_Data{Data: []byte("ID3ok")},
		},
	}, nil
}

func TestServiceRoutesSpeechByRealModel(t *testing.T) {
	speech := &recordingSpeech{}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"tts": {
				Models:     []string{models.Speech28Turbo},
				MinimaxTTS: &config.MinimaxTTSProviderConfig{APIKey: "k"},
			},
		},
	}, map[string]provider.Set{"tts": {Speech: speech}}, nil)

	resp, err := service.SynthesizeSpeech(context.Background(), &modelhubv2.SynthesizeSpeechRequest{
		Model: models.Speech28Turbo,
		Text:  "你好镜子",
	})
	if err != nil {
		t.Fatal(err)
	}
	if speech.model != models.Speech28Turbo || string(resp.GetAudio().GetData()) != "ID3ok" {
		t.Fatalf("model=%q resp=%v", speech.model, resp)
	}
}

func TestServiceRejectsSpeechCapabilityMismatch(t *testing.T) {
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: &recordingText{}}}, nil)
	_, err := service.SynthesizeSpeech(context.Background(), &modelhubv2.SynthesizeSpeechRequest{
		Model: "chat-model",
		Text:  "hi",
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestServiceRejectsEmptySpeechText(t *testing.T) {
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"tts": {
				Models:     []string{models.Speech28Turbo},
				MinimaxTTS: &config.MinimaxTTSProviderConfig{APIKey: "k"},
			},
		},
	}, map[string]provider.Set{"tts": {Speech: &recordingSpeech{}}}, nil)
	_, err := service.SynthesizeSpeech(context.Background(), &modelhubv2.SynthesizeSpeechRequest{
		Model: models.Speech28Turbo,
		Text:  "   ",
	})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestServiceSpeechUpstreamErrorNotPartialSuccess(t *testing.T) {
	speech := &recordingSpeech{err: provider.New(provider.ErrorUnavailable, "upstream boom")}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"tts": {
				Models:     []string{models.Speech28Turbo},
				MinimaxTTS: &config.MinimaxTTSProviderConfig{APIKey: "k"},
			},
		},
	}, map[string]provider.Set{"tts": {Speech: speech}}, nil)
	resp, err := service.SynthesizeSpeech(context.Background(), &modelhubv2.SynthesizeSpeechRequest{
		Model: models.Speech28Turbo,
		Text:  "hi",
	})
	if resp != nil || err == nil {
		t.Fatalf("expected error without response, resp=%v err=%v", resp, err)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
}
