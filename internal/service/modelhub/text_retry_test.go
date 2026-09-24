package modelhub

import (
	"context"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func TestGenerateTextRetriesRateLimitAndUnavailable(t *testing.T) {
	restore := shortenTextRetry(t)
	defer restore()

	text := &scriptedText{failures: []error{
		provider.New(provider.ErrorRateLimited, "HTTP 429"),
		provider.New(provider.ErrorUnavailable, "HTTP 503"),
	}}
	service := textRetryService(text)
	stream := &generateRecorder{ctx: context.Background()}
	if err := service.Generate(textRequest("chat-model", "do"), stream); err != nil {
		t.Fatal(err)
	}
	if text.calls != 3 {
		t.Fatalf("calls=%d want 3", text.calls)
	}
	if len(stream.events) != 1 || stream.events[0].GetItems()[0].GetText() != "ok" {
		t.Fatalf("events=%#v", stream.events)
	}
}

func TestGenerateTextDoesNotRetryTimeout(t *testing.T) {
	restore := shortenTextRetry(t)
	defer restore()

	text := &scriptedText{failures: []error{
		provider.New(provider.ErrorTimeout, "HTTP 504"),
	}}
	service := textRetryService(text)
	err := service.Generate(textRequest("chat-model", "do"), &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("timeout must surface")
	}
	if text.calls != 1 {
		t.Fatalf("calls=%d want 1", text.calls)
	}
}

func TestGenerateTextDoesNotRetryInvalidArgument(t *testing.T) {
	restore := shortenTextRetry(t)
	defer restore()

	text := &scriptedText{failures: []error{
		provider.New(provider.ErrorInvalidArgument, "HTTP 400"),
	}}
	service := textRetryService(text)
	err := service.Generate(textRequest("chat-model", "do"), &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("invalid argument must surface")
	}
	if text.calls != 1 {
		t.Fatalf("calls=%d want 1", text.calls)
	}
}

func TestGenerateTextStopsAfterRetryBudget(t *testing.T) {
	restore := shortenTextRetry(t)
	defer restore()

	text := &scriptedText{failures: []error{
		provider.New(provider.ErrorUnavailable, "HTTP 503"),
		provider.New(provider.ErrorUnavailable, "HTTP 503"),
		provider.New(provider.ErrorUnavailable, "HTTP 503"),
		provider.New(provider.ErrorUnavailable, "HTTP 503"),
	}}
	service := textRetryService(text)
	err := service.Generate(textRequest("chat-model", "do"), &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("exhausted retries must surface")
	}
	if text.calls != textTransientRetries+1 {
		t.Fatalf("calls=%d want %d", text.calls, textTransientRetries+1)
	}
}

func TestGenerateTextDoesNotRetryAfterStreamDelta(t *testing.T) {
	restore := shortenTextRetry(t)
	defer restore()

	text := &scriptedText{streamDelta: "partial", failures: []error{
		provider.New(provider.ErrorUnavailable, "HTTP 503"),
	}}
	service := textRetryService(text)
	request := textRequest("chat-model", "do")
	request.Output.Stream = true
	err := service.Generate(request, &generateRecorder{ctx: context.Background()})
	if err == nil {
		t.Fatal("stream failure after a delta must surface")
	}
	if text.calls != 1 {
		t.Fatalf("calls=%d want 1", text.calls)
	}
}

type scriptedText struct {
	calls       int
	failures    []error
	streamDelta string
}

func (s *scriptedText) Generate(context.Context, string, *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	err := s.next()
	if err != nil {
		return nil, err
	}
	return provider.TextFinalEvent("ok", nil, "resp", "stop", nil), nil
}

func (s *scriptedText) GenerateStream(_ context.Context, _ string, _ *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	err := s.next()
	if s.streamDelta != "" {
		_ = emit(provider.TextDeltaEvent(s.streamDelta))
	}
	return nil, err
}

func (s *scriptedText) next() error {
	index := s.calls
	s.calls++
	if index < len(s.failures) {
		return s.failures[index]
	}
	return nil
}

func textRetryService(text provider.TextProvider) *Service {
	return newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)
}

func shortenTextRetry(t *testing.T) func() {
	t.Helper()
	previous := textTransientRetryDelay
	textTransientRetryDelay = time.Millisecond
	return func() { textTransientRetryDelay = previous }
}
