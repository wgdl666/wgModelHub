package cohereembed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

type embeddingDoer func(*http.Request) (*http.Response, error)

func (d embeddingDoer) Do(r *http.Request) (*http.Response, error) { return d(r) }
func floatResponse(rows [][]float64) *http.Response {
	data, _ := json.Marshal(map[string]any{"embeddings": map[string]any{"float": rows}})
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}
}
func payloadTexts(r *http.Request) []string {
	var payload struct {
		Texts []string `json:"texts"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	return payload.Texts
}
func mustProvider(t *testing.T, cfg config.CohereEmbeddingProviderConfig) *Provider {
	t.Helper()
	cfg.BaseURL = "http://example.invalid"
	cfg.APIKey = "test"
	p, err := New("cohere", &cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func logicalRequest(texts ...string) *modelhubv2.GenerateRequest {
	return textRequest(`{"embedding_batch":true,"input_type":"search_query"}`, texts...)
}

// 大逻辑请求在适配器内依序拆批，不能把调用方一个并发放大成多个上游并发。
func TestBatch500SplitsSequentiallyAndPreservesOrder(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{})
	var starts, sizes []int
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		texts := payloadTexts(r)
		start, _ := strconv.Atoi(texts[0])
		starts = append(starts, start)
		sizes = append(sizes, len(texts))
		rows := testVectors(len(texts))
		for i, text := range texts {
			n, _ := strconv.Atoi(text)
			rows[i][0] = float64(n)
		}
		return floatResponse(rows), nil
	})
	texts := make([]string, 500)
	for i := range texts {
		texts[i] = strconv.Itoa(i)
	}
	event, err := p.Generate(context.Background(), models.CohereEmbedV4, logicalRequest(texts...))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	if err := json.Unmarshal([]byte(event.Items[0].GetText()), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Embeddings) != 500 {
		t.Fatalf("rows=%d", len(decoded.Embeddings))
	}
	for i, row := range decoded.Embeddings {
		if row[0] != float64(i) {
			t.Fatalf("order changed at %d", i)
		}
	}
	if !reflect.DeepEqual(starts, []int{0, 96, 192, 288, 384, 480}) || !reflect.DeepEqual(sizes, []int{96, 96, 96, 96, 96, 20}) {
		t.Fatalf("upstream starts=%v sizes=%v", starts, sizes)
	}
}

// 不同调用方请求互不占共享名额，普通文本和录衣图片不受批量功能额外限流。
func TestIndependentRequestsHaveNoSharedConcurrencyGate(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	var calls atomic.Int64
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return floatResponse(testVectors(1)), nil
	})
	image := textRequest("")
	image.Input.Items = []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{Source: &modelhubv2.Media_Data{Data: []byte("png")}}}}}}}}}
	requests := []*modelhubv2.GenerateRequest{logicalRequest("a", "b"), textRequest("", "coat"), textRequest("", "pants"), textRequest("", `{"color":"black"}`), image}
	done := make(chan error, len(requests))
	for _, request := range requests {
		go func() { _, err := p.Generate(ctx, models.CohereEmbedV4, request); done <- err }()
	}
	for range requests {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("independent requests blocked by shared gate")
		}
	}
	// 首批完成前，批量请求的第二片尚未开始。
	if calls.Load() != int64(len(requests)) {
		t.Fatalf("batch amplified concurrency: calls=%d", calls.Load())
	}
	close(release)
	for range requests {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestFailedChunkStopsWithoutProviderRetryOrPartialResult(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1})
	counts := map[string]int{}
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		text := payloadTexts(r)[0]
		counts[text]++
		if text == "b" {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return floatResponse(testVectors(1)), nil
	})
	event, err := p.Generate(context.Background(), models.CohereEmbedV4, logicalRequest("a", "b", "c"))
	if provider.Kind(err) != provider.ErrorUnavailable || event != nil || !reflect.DeepEqual(counts, map[string]int{"a": 1, "b": 1}) {
		t.Fatalf("err=%v event=%v calls=%v", err, event, counts)
	}
}

func TestAdmissionLimitsRejectWholeRequestBeforeHTTP(t *testing.T) {
	for _, cfg := range []config.CohereEmbeddingProviderConfig{{MaxRequestTexts: 2}, {MaxRequestTextBytes: 2}} {
		p := mustProvider(t, cfg)
		calls := 0
		p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) { calls++; return floatResponse(testVectors(1)), nil })
		_, err := p.Generate(context.Background(), models.CohereEmbedV4, logicalRequest("a", "b", "c"))
		if err == nil || !provider.IsNotAttempted(err) || calls != 0 {
			t.Fatalf("admission sent partial request: err=%v calls=%d", err, calls)
		}
	}
}

func TestBatchCancellationStopsRemainingChunksAndPreservesAttempt(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return floatResponse(testVectors(1)), nil
	})
	event, err := p.Generate(ctx, models.CohereEmbedV4, logicalRequest("a", "b", "c"))
	if !errors.Is(err, context.Canceled) || provider.IsNotAttempted(err) || calls != 1 || event != nil {
		t.Fatalf("err=%v calls=%d event=%v", err, calls, event)
	}
}
