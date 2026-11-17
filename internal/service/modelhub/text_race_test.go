package modelhub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"google.golang.org/protobuf/proto"
)

type delayText struct {
	delay    time.Duration
	text     string
	prefix   *modelhubv2.GenerateEvent
	hold     bool
	mu       sync.Mutex
	canceled bool
	calls    int
	request  *modelhubv2.GenerateRequest
}

func (d *delayText) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	return provider.TextFinalEvent(d.text, nil, "resp", "stop", nil), nil
}

func (d *delayText) GenerateStream(ctx context.Context, _ string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	d.mu.Lock()
	d.calls++
	d.request = request
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
			"vertex_chat": {Models: []string{"gemini-3.5-flash-lite"}, VertexAI: &config.VertexAIProviderConfig{Project: "test-project"}},
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
	scrape := installMetricScraper(t)
	fast := &delayText{delay: 20 * time.Millisecond, text: "fast"}
	slow := &delayText{delay: 2 * time.Second, text: "slow"}
	service := raceService(fast, slow)
	ledger := &callledger.Memory{}
	service.ledger = ledger
	service.ledgerGo = func(fn func()) { fn() }
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
	if len(ledger.Records) != 1 || ledger.Records[0].Provider != "vertex_chat" || ledger.Records[0].Status != callledger.StatusSucceeded {
		t.Fatalf("ledger must retain actual winner: %+v", ledger.Records)
	}
	// 非默认分支获胜也只记入口的一次成功；落败取消不能污染逻辑请求成功率。
	body := scrape()
	for _, metric := range []string{"wg_modelhub_llm_requests_total", "wg_modelhub_llm_terminals_total", "wg_modelhub_llm_ttft_seconds_count"} {
		if metricSum(t, body, metric) != 1 || metricValue(t, body, metric, map[string]string{"provider": "gemini_main"}) != 1 {
			t.Fatalf("race split logical metric %s: %s", metric, body)
		}
	}
	if metricValue(t, body, "wg_modelhub_llm_terminals_total", map[string]string{"outcome": "succeeded"}) != 1 {
		t.Fatal(body)
	}
}

func TestRaceSharesReadOnlyRequestAndImage(t *testing.T) {
	for _, caching := range []*modelhubv2.CachingConfig{nil, {Enabled: true, ExpireAtUnix: 99}, {Enabled: false}} {
		fast := &delayText{delay: time.Millisecond, text: "fast"}
		slow := &delayText{delay: time.Second, text: "slow"}
		service := raceService(fast, slow)
		req := textRequest("gemini-3.5-flash-lite", "identify clothing")
		req.Output.Stream = true
		req.Input.Caching = caching
		req.Input.Items[0].GetMessage().Parts = append(req.Input.Items[0].GetMessage().Parts,
			&modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
				MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: []byte("shared image")},
			}}})
		before := proto.Clone(req)
		if err := service.Generate(req, &generateRecorder{ctx: context.Background()}); err != nil {
			t.Fatal(err)
		}
		if fast.request != req || slow.request != req {
			t.Fatal("both race providers must receive the same original request")
		}
		if !proto.Equal(req, before) {
			t.Fatal("race changed caller fields or image bytes")
		}
	}
}

func TestRaceTerminalCountsOnceWithoutWinner(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		outcome string
	}{
		{"all_failed", errors.New("provider failed"), "failed"},
		{"caller_cancelled", context.Canceled, "cancelled"},
		{"deadline", context.DeadlineExceeded, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scrape := installMetricScraper(t)
			ctx := context.Background()
			if tc.err == context.Canceled {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			service := raceService(&errText{err: tc.err}, &errText{err: tc.err})
			request := textRequest("gemini-3.5-flash-lite", "hi")
			request.Output.Stream = true
			if err := service.Generate(request, &generateRecorder{ctx: ctx}); err == nil {
				t.Fatal("expected terminal error")
			}
			body := scrape()
			if metricSum(t, body, "wg_modelhub_llm_terminals_total") != 1 || metricValue(t, body, "wg_modelhub_llm_terminals_total", map[string]string{"provider": "gemini_main", "outcome": tc.outcome}) != 1 {
				t.Fatal(body)
			}
		})
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
