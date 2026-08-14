package modelhub

import (
	"context"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	"github.com/wgdl666/wgModelHub/internal/profile"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordingText struct {
	profile string
	model   string
}

func (r *recordingText) Generate(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest) (*modelhubv1.GenerateTextResponse, error) {
	r.model = model
	return &modelhubv1.GenerateTextResponse{Content: "ok"}, nil
}

func (r *recordingText) GenerateStream(ctx context.Context, model string, request *modelhubv1.GenerateTextRequest, emit provider.EmitTextEvent) (*modelhubv1.GenerateTextResponse, error) {
	r.model = model
	return &modelhubv1.GenerateTextResponse{}, nil
}

type recordingImage struct {
	model string
}

func (r *recordingImage) GenerateImage(ctx context.Context, model string, request *modelhubv1.GenerateImageRequest) (*modelhubv1.GenerateImageResponse, error) {
	r.model = model
	return &modelhubv1.GenerateImageResponse{}, nil
}

type streamingText struct{}

func (streamingText) Generate(context.Context, string, *modelhubv1.GenerateTextRequest) (*modelhubv1.GenerateTextResponse, error) {
	return nil, nil
}

func (streamingText) GenerateStream(_ context.Context, _ string, _ *modelhubv1.GenerateTextRequest, emit provider.EmitTextEvent) (*modelhubv1.GenerateTextResponse, error) {
	_ = emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_TextChunk{TextChunk: "hello"}})
	// 即使供应商误发 completed，service 也必须收敛成唯一一次最终事件。
	_ = emit(&modelhubv1.TextStreamEvent{Event: &modelhubv1.TextStreamEvent_Completed{
		Completed: &modelhubv1.GenerateTextResponse{Content: "stale"},
	}})
	return &modelhubv1.GenerateTextResponse{Content: "hello", ResponseId: "resp-1"}, nil
}

type textStreamRecorder struct {
	grpc.ServerStream
	ctx    context.Context
	events []*modelhubv1.TextStreamEvent
}

func (r *textStreamRecorder) Context() context.Context {
	return r.ctx
}

func (r *textStreamRecorder) Send(event *modelhubv1.TextStreamEvent) error {
	r.events = append(r.events, event)
	return nil
}

func TestServiceRoutesTextProfile(t *testing.T) {
	text := &recordingText{}
	service := New(config.Config{
		Profiles: map[string]config.ProfileConfig{
			profile.HubChat: {
				Capability: config.CapabilityText,
				Provider:   "ark",
				Model:      "chat-model",
			},
		},
	}, map[string]provider.Set{
		"ark": {Text: text},
	})

	response, err := service.GenerateText(context.Background(), &modelhubv1.GenerateTextRequest{
		ModelProfile: profile.HubChat,
		Messages: []*modelhubv1.Message{
			{
				Role: modelhubv1.Role_ROLE_USER,
				Parts: []*modelhubv1.ContentPart{
					{Content: &modelhubv1.ContentPart_Text{Text: "hi"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "ok" || text.model != "chat-model" {
		t.Fatalf("response=%#v model=%q", response, text.model)
	}
}

func TestServiceRejectsCapabilityMismatch(t *testing.T) {
	service := New(config.Config{
		Profiles: map[string]config.ProfileConfig{
			profile.AsyncArtwork: {
				Capability: config.CapabilityImage,
				Provider:   "gemini",
				Model:      "image-model",
			},
		},
	}, map[string]provider.Set{
		"gemini": {Image: &recordingImage{}},
	})

	_, err := service.GenerateText(context.Background(), &modelhubv1.GenerateTextRequest{
		ModelProfile: profile.AsyncArtwork,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v err = %v", status.Code(err), err)
	}
	st, _ := status.FromError(err)
	var reason string
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			reason = info.Reason
		}
	}
	if reason != string(provider.ErrorInvalidArgument) {
		t.Fatalf("reason = %q", reason)
	}
}

func TestServiceRoutesImageProfile(t *testing.T) {
	image := &recordingImage{}
	service := New(config.Config{
		Profiles: map[string]config.ProfileConfig{
			profile.AsyncArtwork: {
				Capability: config.CapabilityImage,
				Provider:   "gemini",
				Model:      "artwork-model",
			},
		},
	}, map[string]provider.Set{
		"gemini": {Image: image},
	})

	_, err := service.GenerateImage(context.Background(), &modelhubv1.GenerateImageRequest{
		ModelProfile: profile.AsyncArtwork,
	})
	if err != nil {
		t.Fatal(err)
	}
	if image.model != "artwork-model" {
		t.Fatalf("model = %q", image.model)
	}
}

func TestGenerateTextStreamSendsExactlyOneCompletedEvent(t *testing.T) {
	service := New(config.Config{
		Profiles: map[string]config.ProfileConfig{
			profile.HubChat: {
				Capability: config.CapabilityText,
				Provider:   "gemini",
				Model:      "chat-model",
			},
		},
	}, map[string]provider.Set{
		"gemini": {Text: streamingText{}},
	})
	stream := &textStreamRecorder{ctx: context.Background()}
	if err := service.GenerateTextStream(&modelhubv1.GenerateTextRequest{
		ModelProfile: profile.HubChat,
	}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 2 {
		t.Fatalf("events = %#v", stream.events)
	}
	if stream.events[0].GetTextChunk() != "hello" {
		t.Fatalf("first event = %#v", stream.events[0])
	}
	completed := stream.events[1].GetCompleted()
	if completed == nil || completed.GetContent() != "hello" || completed.GetResponseId() != "resp-1" {
		t.Fatalf("completed = %#v", completed)
	}
}

func TestGenerateImageRejectsOversizedInlineMedia(t *testing.T) {
	service := New(config.Config{
		Profiles: map[string]config.ProfileConfig{
			profile.AsyncArtwork: {
				Capability: config.CapabilityImage,
				Provider:   "gemini",
				Model:      "artwork-model",
			},
		},
	}, map[string]provider.Set{
		"gemini": {Image: &recordingImage{}},
	})

	_, err := service.GenerateImage(context.Background(), &modelhubv1.GenerateImageRequest{
		ModelProfile: profile.AsyncArtwork,
		Parts: []*modelhubv1.ContentPart{{
			Content: &modelhubv1.ContentPart_Image{Image: &modelhubv1.Media{
				MimeType: "image/png",
				Source:   &modelhubv1.Media_Data{Data: make([]byte, protocol.MaxMediaBytes+1)},
			}},
		}},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v err = %v", status.Code(err), err)
	}
}
