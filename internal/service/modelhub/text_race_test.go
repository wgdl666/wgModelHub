package modelhub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type delayText struct {
	delay    time.Duration
	text     string
	prefix   *modelhubv2.GenerateEvent
	hold     bool
	mu       sync.Mutex
	canceled bool
	calls    int
}

func (d *delayText) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	return provider.TextFinalEvent(d.text, nil, "resp", "stop", nil), nil
}

func (d *delayText) GenerateStream(ctx context.Context, _ string, _ *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	timer := time.NewTimer(d.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		d.mu.Lock()
		d.canceled = true
		d.mu.Unlock()
		return nil, ctx.Err()
	case <-timer.C:
	}
	if d.prefix != nil {
		if err := emit(d.prefix); err != nil {
			return nil, err
		}
	}
	if d.hold {
		<-ctx.Done()
		d.mu.Lock()
		d.canceled = true
		d.mu.Unlock()
		return nil, ctx.Err()
	}
	if err := emit(provider.TextDeltaEvent(d.text)); err != nil {
		return nil, err
	}
	return provider.MetadataFinalEvent("resp", "stop", nil), nil
}

func (d *delayText) wasCanceled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.canceled
}

func raceService(fast, slow provider.TextProvider) *Service {
	return newTestService(config.Config{
		Logfire:  config.LogfireConfig{Token: "t", Env: "test", Service: "wg-model-hub"},
		Database: config.DatabaseConfig{DSN: "postgres://modelhub:modelhub@127.0.0.1:5432/modelhub?sslmode=disable"},
		Providers: map[string]config.ProviderConfig{
			"gemini_main": {Models: []string{"gemini-3.5-flash-lite"}, Gemini: &config.GeminiProviderConfig{APIKey: "a"}},
			"vertex_chat": {Models: []string{"gemini-3.5-flash-lite"}, VertexAI: &config.VertexAIProviderConfig{APIKey: "b"}},
		},
		ModelRouteOverrides: map[string]config.ModelRoute{
			"gemini-3.5-flash-lite": {Default: "gemini_main", Race: []string{"gemini_main", "vertex_chat"}},
		},
	}, map[string]provider.Set{
		"gemini_main": {Text: slow},
		"vertex_chat": {Text: fast},
	}, nil)
}

func TestRaceUsesFirstTokenAndCancelsLoser(t *testing.T) {
	fast := &delayText{delay: 20 * time.Millisecond, text: "fast"}
	slow := &delayText{delay: 2 * time.Second, text: "slow"}
	service := raceService(fast, slow)
	request := textRequest("gemini-3.5-flash-lite", "hi")
	request.Output.Stream = true
	stream := &generateRecorder{ctx: context.Background()}
	if err := service.Generate(request, stream); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, event := range stream.events {
		for _, item := range event.GetItems() {
			if text := item.GetText(); text != "" {
				texts = append(texts, text)
			}
		}
	}
	if len(texts) != 1 || texts[0] != "fast" {
		t.Fatalf("texts=%v", texts)
	}
	if !slow.wasCanceled() {
		t.Fatal("slow provider must be canceled after the fast first token")
	}
}

func TestRaceDropsLoserEventsBeforeFirstToken(t *testing.T) {
	fast := &delayText{delay: 40 * time.Millisecond, text: "fast"}
	slow := &delayText{delay: 5 * time.Millisecond, prefix: provider.TextDeltaEvent(" "), hold: true}
	service := raceService(fast, slow)
	request := textRequest("gemini-3.5-flash-lite", "hi")
	request.Output.Stream = true
	stream := &generateRecorder{ctx: context.Background()}
	if err := service.Generate(request, stream); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, event := range stream.events {
		for _, item := range event.GetItems() {
			if text := item.GetText(); text != "" {
				texts = append(texts, text)
			}
		}
	}
	if len(texts) != 1 || texts[0] != "fast" {
		t.Fatalf("texts=%q", texts)
	}
}

func TestRaceUnsetFallsBackToDefault(t *testing.T) {
	fast := &delayText{delay: time.Millisecond, text: "fast"}
	slow := &delayText{delay: time.Millisecond, text: "slow"}
	service := raceService(fast, slow)
	request := textRequest("gemini-3.5-flash-lite", "hi")
	request.Output.Stream = true
	service.live.Store(func() config.Config {
		cfg := service.live.Load()
		route := cfg.ModelRouteOverrides["gemini-3.5-flash-lite"]
		route.Race = nil
		cfg.ModelRouteOverrides["gemini-3.5-flash-lite"] = route
		return cfg
	}())
	stream := &generateRecorder{ctx: context.Background()}
	if err := service.Generate(request, stream); err != nil {
		t.Fatal(err)
	}
	if fast.calls != 0 {
		t.Fatal("vertex must not be called when race list is empty")
	}
	if slow.calls != 1 {
		t.Fatalf("default calls=%d", slow.calls)
	}
}
