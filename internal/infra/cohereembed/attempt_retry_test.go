package cohereembed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// hang 模拟供应商一直不回包：只在本次请求的 ctx 结束时返回。
func hang(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func shortAttemptProvider(t *testing.T, cfg config.CohereEmbeddingProviderConfig) *Provider {
	t.Helper()
	p := mustProvider(t, cfg)
	p.attemptTimeout = 20 * time.Millisecond
	return p
}

func TestHungAttemptIsResentOnceAndSucceeds(t *testing.T) {
	p := shortAttemptProvider(t, config.CohereEmbeddingProviderConfig{})
	calls := 0
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return hang(r)
		}
		return floatResponse(testVectors(1)), nil
	})
	got, err := p.post(context.Background(), map[string]any{}, 1)
	if err != nil || len(got) != 1 || calls != 2 {
		t.Fatalf("err=%v vectors=%d calls=%d", err, len(got), calls)
	}
}

func TestTwoHungAttemptsReturnTimeoutWithoutThirdSend(t *testing.T) {
	p := shortAttemptProvider(t, config.CohereEmbeddingProviderConfig{})
	calls := 0
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) { calls++; return hang(r) })
	_, err := p.post(context.Background(), map[string]any{}, 1)
	if !errors.Is(err, context.DeadlineExceeded) || provider.IsNotAttempted(err) || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	// 调用方看到的仍是原来的超时状态，Service 的同步文本重试不会再放大。
	if code := status.Code(provider.ToStatus(err)); code != codes.DeadlineExceeded {
		t.Fatalf("status code=%v", code)
	}
}

func TestCallerCancelOrDeadlineIsNotResent(t *testing.T) {
	for name, newCtx := range map[string]func() (context.Context, context.CancelFunc){
		"cancel": func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
		"deadline": func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 5*time.Millisecond)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := shortAttemptProvider(t, config.CohereEmbeddingProviderConfig{})
			p.attemptTimeout = time.Second
			ctx, cancel := newCtx()
			defer cancel()
			calls := 0
			p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
				calls++
				if name == "cancel" {
					cancel()
				}
				return hang(r)
			})
			_, err := p.post(ctx, map[string]any{}, 1)
			if err == nil || calls != 1 || !errors.Is(err, ctx.Err()) {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestProviderAnswersAndConnectionErrorsAreNotResent(t *testing.T) {
	for name, doer := range map[string]embeddingDoer{
		"rate_limited": func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(""))}, nil
		},
		"unavailable": func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		},
		"connection": func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset by peer") },
	} {
		t.Run(name, func(t *testing.T) {
			p := shortAttemptProvider(t, config.CohereEmbeddingProviderConfig{})
			calls := 0
			p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) { calls++; return doer(r) })
			_, err := p.post(context.Background(), map[string]any{}, 1)
			if err == nil || calls != 1 || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

// 供应商先回 200 头、正文卡住时，单次时限也要覆盖读正文，并且同样只重发一次。
// 走真实 HTTP 传输层，确认读正文超时也被识别为本次超时。
func TestStalledBodyOverRealTransportIsResentOnce(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"embeddings":{"float":[[`))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		data, _ := json.Marshal(map[string]any{"embeddings": map[string]any{"float": testVectors(1)}})
		_, _ = w.Write(data)
	}))
	defer server.Close()

	p, err := New("cohere", &config.CohereEmbeddingProviderConfig{BaseURL: server.URL, APIKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	p.attemptTimeout = 200 * time.Millisecond
	got, err := p.post(context.Background(), map[string]any{}, 1)
	if err != nil || len(got) != 1 || calls.Load() != 2 {
		t.Fatalf("err=%v vectors=%d calls=%d", err, len(got), calls.Load())
	}
}

func TestHungBatchChunkIsResentAndKeepsOrder(t *testing.T) {
	p := shortAttemptProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1})
	counts := map[string]int{}
	var order []string
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		text := payloadTexts(r)[0]
		counts[text]++
		if text == "b" && counts[text] == 1 {
			return hang(r)
		}
		order = append(order, text)
		return floatResponse(testVectors(1)), nil
	})
	event, err := p.Generate(context.Background(), models.CohereEmbedV4, logicalRequest("a", "b", "c"))
	if err != nil || event == nil {
		t.Fatalf("err=%v event=%v", err, event)
	}
	if !reflect.DeepEqual(counts, map[string]int{"a": 1, "b": 2, "c": 1}) || !reflect.DeepEqual(order, []string{"a", "b", "c"}) {
		t.Fatalf("counts=%v order=%v", counts, order)
	}
}
