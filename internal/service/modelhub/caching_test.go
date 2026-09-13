package modelhub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
	"github.com/wgdl666/wgModelHub/internal/infra/metricserver"
	"github.com/wgdl666/wgModelHub/internal/provider"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
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

func TestApplyTextCachingPolicy(t *testing.T) {
	plainArk := config.ProviderConfig{Ark: &config.ArkProviderConfig{APIKey: "k"}}
	endpointArk := config.ProviderConfig{Ark: &config.ArkProviderConfig{APIKey: "k", EndpointID: "ep-test"}}

	req := &modelhubv2.GenerateRequest{}
	if mode := applyTextCachingPolicy(req, plainArk); mode != cachingModeDefaultEnabled {
		t.Fatalf("nil input mode = %q", mode)
	}
	if !req.GetInput().GetCaching().GetEnabled() {
		t.Fatalf("nil input should default enable: %#v", req.Input)
	}

	req = &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{}}
	if mode := applyTextCachingPolicy(req, plainArk); mode != cachingModeDefaultEnabled {
		t.Fatalf("omit mode = %q", mode)
	}
	if !req.Input.Caching.Enabled || req.Input.Caching.ExpireAtUnix != 0 {
		t.Fatalf("omit caching should enable without inventing expire: %#v", req.Input.Caching)
	}

	req = &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Caching: &modelhubv2.CachingConfig{Enabled: true, ExpireAtUnix: 99}}}
	if mode := applyTextCachingPolicy(req, plainArk); mode != cachingModeExplicitEnabled {
		t.Fatalf("plain explicit on mode = %q", mode)
	}
	if !req.Input.Caching.Enabled || req.Input.Caching.ExpireAtUnix != 99 {
		t.Fatalf("explicit enabled must keep expire_at: %#v", req.Input.Caching)
	}

	req = &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Caching: &modelhubv2.CachingConfig{Enabled: false, ExpireAtUnix: 99}}}
	if mode := applyTextCachingPolicy(req, plainArk); mode != cachingModeExplicitDisabled {
		t.Fatalf("plain explicit off mode = %q", mode)
	}
	if req.Input.Caching.Enabled {
		t.Fatalf("explicit disabled must stay off: %#v", req.Input.Caching)
	}

	for _, tc := range []struct {
		name string
		cfg  *modelhubv2.CachingConfig
	}{
		{name: "omit", cfg: nil},
		{name: "explicit_on", cfg: &modelhubv2.CachingConfig{Enabled: true, ExpireAtUnix: 99}},
		{name: "explicit_off", cfg: &modelhubv2.CachingConfig{Enabled: false, ExpireAtUnix: 99}},
	} {
		t.Run("endpoint_"+tc.name, func(t *testing.T) {
			req := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Caching: tc.cfg}}
			if mode := applyTextCachingPolicy(req, endpointArk); mode != cachingModeImplicitAutomatic {
				t.Fatalf("mode = %q want %q", mode, cachingModeImplicitAutomatic)
			}
			if req.Input.Caching != nil {
				t.Fatalf("endpoint-bound must omit explicit caching field: %#v", req.Input.Caching)
			}
		})
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

type scriptedStreamText struct {
	events []*modelhubv2.GenerateEvent
	err    error
	usage  *modelhubv2.Usage
}

func (s *scriptedStreamText) Generate(context.Context, string, *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	return nil, errors.New("non-stream unused")
}

func (s *scriptedStreamText) GenerateStream(_ context.Context, _ string, _ *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	for _, event := range s.events {
		if err := emit(event); err != nil {
			return nil, err
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return provider.MetadataFinalEvent("resp", "stop", s.usage), nil
}

type errText struct {
	err error
}

func (e *errText) Generate(context.Context, string, *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	return nil, e.err
}

func (e *errText) GenerateStream(context.Context, string, *modelhubv2.GenerateRequest, provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	return nil, e.err
}

func installMetricScraper(t *testing.T) func() string {
	t.Helper()
	reader, err := metricserver.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader.ManualReader()))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	if err := llmmetric.Configure(mp.Meter("modelhub-test")); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(reader.Handler())
	t.Cleanup(ts.Close)
	return func() string {
		resp, err := http.Get(ts.URL)
		if err != nil {
			t.Fatalf("scrape: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(body)
	}
}

func metricValue(t *testing.T, text, name string, labels map[string]string) float64 {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		ok := true
		for k, v := range labels {
			if !strings.Contains(line, k+`="`+v+`"`) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return val
	}
	t.Fatalf("metric %s labels=%v not found in:\n%s", name, labels, text)
	return 0
}

func metricSum(t *testing.T, text, name string) float64 {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `(?:\{[^}]*\})?\s+([0-9.eE+-]+)`)
	var sum float64
	found := false
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		val, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatal(err)
		}
		sum += val
		found = true
	}
	if !found {
		return 0
	}
	return sum
}

func TestGenerateTextDefaultsCachingAndRecordsUsage(t *testing.T) {
	scrape := installMetricScraper(t)
	text := &usageText{usage: &modelhubv2.Usage{InputTokens: 100, OutputTokens: 5, TotalTokens: 105, CachedTokens: 40, ReasoningTokens: 2}}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)

	if err := service.Generate(textRequest("chat-model", "do"), &generateRecorder{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if text.request.GetInput().GetCaching() == nil || !text.request.GetInput().GetCaching().GetEnabled() {
		t.Fatalf("provider should see default enabled caching: %#v", text.request.GetInput())
	}
	body := scrape()
	base := map[string]string{"model": "chat-model", "provider": "ark", "stream": "false"}
	if metricValue(t, body, "wg_modelhub_llm_requests_total", base) != 1 {
		t.Fatal("request not counted once")
	}
	if metricValue(t, body, "wg_modelhub_llm_terminals_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "outcome": "succeeded",
	}) != 1 {
		t.Fatal("terminal missing")
	}
	if metricValue(t, body, "wg_modelhub_llm_usage_reports_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "state": "present",
	}) != 1 {
		t.Fatal("usage present missing")
	}
	if metricValue(t, body, "wg_modelhub_llm_cache_requests_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "result": "hit",
	}) != 1 {
		t.Fatal("cache hit missing")
	}
	if metricValue(t, body, "wg_modelhub_llm_input_tokens_total", base) != 100 ||
		metricValue(t, body, "wg_modelhub_llm_output_tokens_total", base) != 5 ||
		metricValue(t, body, "wg_modelhub_llm_tokens_reported_total", base) != 105 ||
		metricValue(t, body, "wg_modelhub_llm_cached_tokens_total", base) != 40 ||
		metricValue(t, body, "wg_modelhub_llm_reasoning_tokens_total", base) != 2 {
		t.Fatalf("token totals wrong:\n%s", body)
	}
	if strings.Contains(body, "wg_modelhub_llm_ttft_seconds_count") {
		t.Fatal("non-stream must not record TTFT")
	}
}

func TestGenerateTextHonorsExplicitCachingSwitch(t *testing.T) {
	cases := []struct {
		name   string
		cfg    *modelhubv2.CachingConfig
		result string
	}{
		{name: "explicit_on_miss", cfg: &modelhubv2.CachingConfig{Enabled: true, ExpireAtUnix: 123}, result: "miss"},
		{name: "explicit_off_disabled", cfg: &modelhubv2.CachingConfig{Enabled: false, ExpireAtUnix: 123}, result: "disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scrape := installMetricScraper(t)
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
			body := scrape()
			if metricValue(t, body, "wg_modelhub_llm_cache_requests_total", map[string]string{
				"model": "chat-model", "provider": "ark", "stream": "false", "result": tc.result,
			}) != 1 {
				t.Fatalf("cache result=%s missing\n%s", tc.result, body)
			}
		})
	}
}

func TestGenerateTextEndpointBoundUsesImplicitAutomatic(t *testing.T) {
	scrape := installMetricScraper(t)
	text := &usageText{usage: &modelhubv2.Usage{InputTokens: 10}}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k", EndpointID: "ep-test"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)
	req := textRequest("chat-model", "do")
	req.Input.Caching = &modelhubv2.CachingConfig{Enabled: false}
	if err := service.Generate(req, &generateRecorder{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	if text.request.GetInput().GetCaching() != nil {
		t.Fatalf("provider must see nil caching: %#v", text.request.GetInput().GetCaching())
	}
	body := scrape()
	if metricValue(t, body, "wg_modelhub_llm_cache_requests_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "result": "miss",
	}) != 1 {
		t.Fatalf("implicit automatic must not be disabled:\n%s", body)
	}
}

func TestGenerateTextStreamAndNonStreamUsageSame(t *testing.T) {
	usage := &modelhubv2.Usage{InputTokens: 80, CachedTokens: 20, OutputTokens: 3, TotalTokens: 83, ReasoningTokens: 1}
	readTokens := func(stream bool) map[string]float64 {
		scrape := installMetricScraper(t)
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
		body := scrape()
		labels := map[string]string{"model": "chat-model", "provider": "ark", "stream": strconv.FormatBool(stream)}
		return map[string]float64{
			"input":     metricValue(t, body, "wg_modelhub_llm_input_tokens_total", labels),
			"cached":    metricValue(t, body, "wg_modelhub_llm_cached_tokens_total", labels),
			"output":    metricValue(t, body, "wg_modelhub_llm_output_tokens_total", labels),
			"total":     metricValue(t, body, "wg_modelhub_llm_tokens_reported_total", labels),
			"reasoning": metricValue(t, body, "wg_modelhub_llm_reasoning_tokens_total", labels),
		}
	}
	nonStream := readTokens(false)
	stream := readTokens(true)
	for _, key := range []string{"input", "cached", "output", "total", "reasoning"} {
		if nonStream[key] != stream[key] {
			t.Fatalf("%s mismatch: non-stream=%v stream=%v", key, nonStream[key], stream[key])
		}
	}
}

func TestGenerateTextRecordsUsageEvenIfFinalSendFails(t *testing.T) {
	scrape := installMetricScraper(t)
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
	body := scrape()
	if metricValue(t, body, "wg_modelhub_llm_cached_tokens_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false",
	}) != 5 {
		t.Fatalf("usage must survive final Send failure:\n%s", body)
	}
	if metricValue(t, body, "wg_modelhub_llm_terminals_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "outcome": "failed",
	}) != 1 {
		t.Fatalf("send failure must be failed terminal:\n%s", body)
	}
}

func TestGenerateTextUsageMissingUnknownCache(t *testing.T) {
	scrape := installMetricScraper(t)
	text := &usageText{usage: nil}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)
	if err := service.Generate(textRequest("chat-model", "do"), &generateRecorder{ctx: context.Background()}); err != nil {
		t.Fatal(err)
	}
	body := scrape()
	if metricValue(t, body, "wg_modelhub_llm_usage_reports_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "state": "missing",
	}) != 1 {
		t.Fatal("usage missing not recorded")
	}
	if metricValue(t, body, "wg_modelhub_llm_cache_requests_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "result": "unknown",
	}) != 1 {
		t.Fatal("cache unknown not recorded")
	}
	if metricSum(t, body, "wg_modelhub_llm_input_tokens_total") != 0 {
		t.Fatal("missing usage must not invent token counters")
	}
}

func TestGenerateTextStreamTTFTCases(t *testing.T) {
	cases := []struct {
		name      string
		events    []*modelhubv2.GenerateEvent
		err       error
		wantTTFT  bool
		wantCount float64
	}{
		{
			name:     "empty_delta_then_text",
			events:   []*modelhubv2.GenerateEvent{provider.TextDeltaEvent(""), provider.TextDeltaEvent("hi"), provider.TextDeltaEvent(" again")},
			wantTTFT: true, wantCount: 1,
		},
		{
			name:     "first_tool_name",
			events:   []*modelhubv2.GenerateEvent{provider.ToolCallEvent(&modelhubv2.ToolCall{Name: "closet_search"})},
			wantTTFT: true, wantCount: 1,
		},
		{
			name:     "only_tool_arguments",
			events:   []*modelhubv2.GenerateEvent{provider.ToolCallEvent(&modelhubv2.ToolCall{ArgumentsJson: []byte(`{"q":"a"}`)})},
			wantTTFT: false,
		},
		{
			name:     "error_before_first_output",
			events:   []*modelhubv2.GenerateEvent{provider.TextDeltaEvent("")},
			err:      errors.New("upstream boom"),
			wantTTFT: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scrape := installMetricScraper(t)
			text := &scriptedStreamText{events: tc.events, err: tc.err, usage: &modelhubv2.Usage{InputTokens: 1}}
			service := newTestService(config.Config{
				Providers: map[string]config.ProviderConfig{
					"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
				},
			}, map[string]provider.Set{"ark": {Text: text}}, nil)
			req := textRequest("chat-model", "do")
			req.Output.Stream = true
			_ = service.Generate(req, &generateRecorder{ctx: context.Background()})
			body := scrape()
			count := metricSum(t, body, "wg_modelhub_llm_ttft_seconds_count")
			if tc.wantTTFT {
				if count != tc.wantCount {
					t.Fatalf("ttft count=%v want %v\n%s", count, tc.wantCount, body)
				}
			} else if count != 0 {
				t.Fatalf("unexpected ttft count=%v\n%s", count, body)
			}
		})
	}
}

func TestGenerateTextOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		outcome string
	}{
		{name: "failed", err: errors.New("boom"), outcome: "failed"},
		// 真实契约：供应商侧 context 取消/超时经 ToStatus 后再 MapOutcome；不伪造裸 gRPC status。
		{name: "cancelled", err: context.Canceled, outcome: "cancelled"},
		{name: "deadline", err: context.DeadlineExceeded, outcome: "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scrape := installMetricScraper(t)
			service := newTestService(config.Config{
				Providers: map[string]config.ProviderConfig{
					"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
				},
			}, map[string]provider.Set{"ark": {Text: &errText{err: tc.err}}}, nil)
			_ = service.Generate(textRequest("chat-model", "do"), &generateRecorder{ctx: context.Background()})
			body := scrape()
			if metricValue(t, body, "wg_modelhub_llm_terminals_total", map[string]string{
				"model": "chat-model", "provider": "ark", "stream": "false", "outcome": tc.outcome,
			}) != 1 {
				t.Fatalf("outcome %s missing\n%s", tc.outcome, body)
			}
			if metricSum(t, body, "wg_modelhub_llm_usage_reports_total") != 0 {
				t.Fatal("failed request must not record usage")
			}
		})
	}
}

func TestGenerateTextCountsOncePerRequest(t *testing.T) {
	scrape := installMetricScraper(t)
	text := &usageText{usage: &modelhubv2.Usage{InputTokens: 1}}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: text}}, nil)
	for i := 0; i < 3; i++ {
		if err := service.Generate(textRequest("chat-model", "do"), &generateRecorder{ctx: context.Background()}); err != nil {
			t.Fatal(err)
		}
	}
	body := scrape()
	if metricValue(t, body, "wg_modelhub_llm_requests_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false",
	}) != 3 {
		t.Fatalf("requests not 3:\n%s", body)
	}
	if metricValue(t, body, "wg_modelhub_llm_terminals_total", map[string]string{
		"model": "chat-model", "provider": "ark", "stream": "false", "outcome": "succeeded",
	}) != 3 {
		t.Fatalf("terminals not 3:\n%s", body)
	}
}

func TestGenerateImageDoesNotApplyTextCachingPolicy(t *testing.T) {
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

func TestDeployManifestExposesHTTPPort(t *testing.T) {
	// 轻量契约：ack.yaml 必须声明 HTTP 拓扑且 external 不暴露 51053。
	content, err := os.ReadFile("../../../deploy/ack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "WG_SERVER_HTTP_PORT") || !strings.Contains(text, "value: \"51053\"") {
		t.Fatal("missing WG_SERVER_HTTP_PORT=51053")
	}
	if !strings.Contains(text, "name: http") || !strings.Contains(text, "containerPort: 51053") {
		t.Fatal("missing container http port")
	}
	if !strings.Contains(text, "app.kubernetes.io/name: wg-model-hub") {
		t.Fatal("internal service missing ServiceMonitor selector label")
	}
	externalIdx := strings.Index(text, "name: wg-model-hub-external")
	if externalIdx < 0 {
		t.Fatal("external service missing")
	}
	external := text[externalIdx:]
	portsIdx := strings.Index(external, "ports:")
	if portsIdx < 0 {
		t.Fatal("external ports missing")
	}
	if strings.Contains(external[portsIdx:], "51053") {
		t.Fatal("external LoadBalancer must not expose 51053")
	}
}
