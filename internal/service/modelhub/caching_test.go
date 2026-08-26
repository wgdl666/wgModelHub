package modelhub

import (
	"context"
	"errors"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTextCachingMode(t *testing.T) {
	if got := textCachingMode(nil); got != cachingModeDefaultEnabled {
		t.Fatalf("nil input = %q", got)
	}
	if got := textCachingMode(&modelhubv2.Input{}); got != cachingModeDefaultEnabled {
		t.Fatalf("nil caching = %q", got)
	}
	if got := textCachingMode(&modelhubv2.Input{Caching: &modelhubv2.CachingConfig{Enabled: true}}); got != cachingModeExplicitEnabled {
		t.Fatalf("explicit true = %q", got)
	}
	if got := textCachingMode(&modelhubv2.Input{Caching: &modelhubv2.CachingConfig{Enabled: false}}); got != cachingModeExplicitDisabled {
		t.Fatalf("explicit false = %q", got)
	}
}

func TestApplyTextCachingDefault(t *testing.T) {
	req := &modelhubv2.GenerateRequest{}
	applyTextCachingDefault(req)
	if !req.GetInput().GetCaching().GetEnabled() {
		t.Fatalf("nil input should default enable: %#v", req.Input)
	}

	req = &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{}}
	applyTextCachingDefault(req)
	if !req.Input.Caching.Enabled || req.Input.Caching.ExpireAtUnix != 0 {
		t.Fatalf("omit caching should enable without inventing expire: %#v", req.Input.Caching)
	}

	req = &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Caching: &modelhubv2.CachingConfig{Enabled: true, ExpireAtUnix: 99}}}
	applyTextCachingDefault(req)
	if !req.Input.Caching.Enabled || req.Input.Caching.ExpireAtUnix != 99 {
		t.Fatalf("explicit enabled must keep expire_at: %#v", req.Input.Caching)
	}

	req = &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Caching: &modelhubv2.CachingConfig{Enabled: false, ExpireAtUnix: 99}}}
	applyTextCachingDefault(req)
	if req.Input.Caching.Enabled {
		t.Fatalf("explicit disabled must stay off: %#v", req.Input.Caching)
	}
}

type usageText struct {
	recordingText
	usage *modelhubv2.Usage
}

func (u *usageText) Generate(_ context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	u.model = model
	u.request = request
	return provider.TextFinalEvent("ok", nil, "resp", "stop", u.usage), nil
}

func (u *usageText) GenerateStream(_ context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	u.model = model
	u.request = request
	if emit != nil {
		_ = emit(provider.TextDeltaEvent("hi"))
	}
	return provider.MetadataFinalEvent("resp", "stop", u.usage), nil
}

type failFinalSend struct {
	generateRecorder
}

func (f *failFinalSend) Send(event *modelhubv2.GenerateEvent) error {
	if event.GetFinal() {
		return errors.New("client send failed")
	}
	return f.generateRecorder.Send(event)
}

func installSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(prev)
		_ = provider.Shutdown(context.Background())
	})
	return recorder
}

func attrMap(span sdktrace.ReadOnlySpan) map[string]attribute.Value {
	out := make(map[string]attribute.Value, len(span.Attributes()))
	for _, item := range span.Attributes() {
		out[string(item.Key)] = item.Value
	}
	return out
}

func findGenerateSpan(t *testing.T, recorder *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() == "modelhub.Generate" {
			return span
		}
	}
	t.Fatal("modelhub.Generate span missing")
	return nil
}

func TestGenerateTextDefaultsCachingAndRecordsUsage(t *testing.T) {
	recorder := installSpanRecorder(t)
	text := &usageText{usage: &modelhubv2.Usage{InputTokens: 100, CachedTokens: 40}}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)

	stream := &generateRecorder{ctx: context.Background()}
	if err := service.Generate(textRequest("chat-model", "do"), stream); err != nil {
		t.Fatal(err)
	}
	if text.request.GetInput().GetCaching() == nil || !text.request.GetInput().GetCaching().GetEnabled() {
		t.Fatalf("provider should see default enabled caching: %#v", text.request.GetInput())
	}
	span := findGenerateSpan(t, recorder)
	attrs := attrMap(span)
	if attrs[attrCachingMode].AsString() != cachingModeDefaultEnabled {
		t.Fatalf("caching mode = %v", attrs[attrCachingMode])
	}
	if attrs[attrModel].AsString() != "chat-model" || attrs[attrProvider].AsString() != "ark" {
		t.Fatalf("model/provider attrs = %#v", attrs)
	}
	if !attrs[attrUsagePresent].AsBool() || attrs[attrInputTokens].AsInt64() != 100 ||
		attrs[attrCachedTokens].AsInt64() != 40 || !attrs[attrCacheHit].AsBool() {
		t.Fatalf("usage attrs = %#v", attrs)
	}
}

func TestGenerateTextHonorsExplicitCachingSwitch(t *testing.T) {
	cases := []struct {
		name string
		cfg  *modelhubv2.CachingConfig
		mode string
		want bool
	}{
		{name: "explicit_on", cfg: &modelhubv2.CachingConfig{Enabled: true, ExpireAtUnix: 123}, mode: cachingModeExplicitEnabled, want: true},
		{name: "explicit_off", cfg: &modelhubv2.CachingConfig{Enabled: false, ExpireAtUnix: 123}, mode: cachingModeExplicitDisabled, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := installSpanRecorder(t)
			text := &usageText{usage: &modelhubv2.Usage{InputTokens: 10}}
			service := newTestService(config.Config{
				Providers: map[string]config.ProviderConfig{
					"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
				},
			}, map[string]provider.Set{"ark": {Text: text}}, nil)
			req := textRequest("chat-model", "do")
			req.Input.Caching = tc.cfg
			if err := service.Generate(req, &generateRecorder{ctx: context.Background()}); err != nil {
				t.Fatal(err)
			}
			got := text.request.GetInput().GetCaching()
			if got.GetEnabled() != tc.want || got.GetExpireAtUnix() != tc.cfg.ExpireAtUnix {
				t.Fatalf("caching = %#v", got)
			}
			attrs := attrMap(findGenerateSpan(t, recorder))
			if attrs[attrCachingMode].AsString() != tc.mode {
				t.Fatalf("mode = %v", attrs[attrCachingMode])
			}
			if attrs[attrCacheHit].AsBool() {
				t.Fatal("cached_tokens=0 must not count as hit")
			}
		})
	}
}

func TestGenerateTextStreamAndNonStreamUsageAttrsMatch(t *testing.T) {
	usage := &modelhubv2.Usage{InputTokens: 80, CachedTokens: 20}
	readAttrs := func(stream bool) map[string]attribute.Value {
		recorder := installSpanRecorder(t)
		text := &usageText{usage: usage}
		service := newTestService(config.Config{
			Providers: map[string]config.ProviderConfig{
				"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
			},
		}, map[string]provider.Set{"ark": {Text: text}}, nil)
		req := textRequest("chat-model", "do")
		req.Output.Stream = stream
		if err := service.Generate(req, &generateRecorder{ctx: context.Background()}); err != nil {
			t.Fatal(err)
		}
		return attrMap(findGenerateSpan(t, recorder))
	}
	nonStream := readAttrs(false)
	stream := readAttrs(true)
	for _, key := range []string{attrCachingMode, attrInputTokens, attrCachedTokens, attrCacheHit, attrUsagePresent, attrModel, attrProvider, attrCapability} {
		if nonStream[key] != stream[key] {
			t.Fatalf("%s mismatch: non-stream=%v stream=%v", key, nonStream[key], stream[key])
		}
	}
}

func TestGenerateTextRecordsUsageEvenIfFinalSendFails(t *testing.T) {
	recorder := installSpanRecorder(t)
	text := &usageText{usage: &modelhubv2.Usage{InputTokens: 50, CachedTokens: 5}}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)
	err := service.Generate(textRequest("chat-model", "do"), &failFinalSend{generateRecorder: generateRecorder{ctx: context.Background()}})
	if err == nil {
		t.Fatal("expected send failure")
	}
	attrs := attrMap(findGenerateSpan(t, recorder))
	if attrs[attrCachedTokens].AsInt64() != 5 || !attrs[attrCacheHit].AsBool() {
		t.Fatalf("usage must be recorded before Send failure: %#v", attrs)
	}
}

func TestGenerateImageDoesNotApplyTextCachingDefault(t *testing.T) {
	image := &recordingImage{}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {Models: []string{"img-model"}, OpenAI: &config.OpenAIProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"openai": {Image: image}}, nil)
	req := &modelhubv2.GenerateRequest{
		Model:  "img-model",
		Input:  &modelhubv2.Input{},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	}
	if err := service.Generate(req, &generateRecorder{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if req.Input.Caching != nil {
		t.Fatalf("image path must not invent caching: %#v", req.Input.Caching)
	}
}
