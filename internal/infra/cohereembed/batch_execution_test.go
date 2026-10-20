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
	"sync"
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
	p.retryDelay = time.Millisecond
	return p
}
func logicalRequest(texts ...string) *modelhubv2.GenerateRequest {
	return textRequest(`{"embedding_batch":true,"input_type":"search_query"}`, texts...)
}
func waitStarted(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not start")
	}
}

func TestBatch500SplitsInsideProviderAndPreservesOrder(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{})
	var mu sync.Mutex
	sizes := map[int]int{}
	secondFinished := make(chan struct{})
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		texts := payloadTexts(r)
		start, _ := strconv.Atoi(texts[0])
		mu.Lock()
		sizes[start] = len(texts)
		mu.Unlock()
		// 第二片先返回，保证验证的是输入顺序而非上游完成顺序。
		if start == 0 {
			select {
			case <-secondFinished:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		rows := testVectors(len(texts))
		for i, text := range texts {
			n, _ := strconv.Atoi(text)
			rows[i][0] = float64(n)
		}
		if start == 96 {
			close(secondFinished)
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
	if !reflect.DeepEqual(sizes, map[int]int{0: 96, 96: 96, 192: 96, 288: 96, 384: 96, 480: 20}) {
		t.Fatalf("upstream batches=%v", sizes)
	}
}

func TestConcurrencyIsSharedAcrossBatchSingleTextAndImage(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1, MaxConcurrency: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	started := make(chan struct{}, 16)
	var active, peak, calls atomic.Int64
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old; old = peak.Load() {
			if peak.CompareAndSwap(old, current) {
				break
			}
		}
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
	requests := []*modelhubv2.GenerateRequest{logicalRequest("a", "b", "c"), textRequest("", `{"color":"black"}`), image}
	done := make(chan error, len(requests))
	for _, request := range requests {
		go func() { _, err := p.Generate(ctx, models.CohereEmbedV4, request); done <- err }()
	}
	waitStarted(t, started)
	waitStarted(t, started)
	if calls.Load() != 2 || peak.Load() > 2 {
		t.Fatal("requests bypassed shared gate")
	}
	close(release)
	for range requests {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 5 || peak.Load() != 2 {
		t.Fatalf("calls=%d peak=%d", calls.Load(), peak.Load())
	}
}

func TestFailedChunkRetriesWithoutRepeatingSuccessfulChunks(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1, MaxConcurrency: 1})
	counts := map[string]int{}
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		text := payloadTexts(r)[0]
		counts[text]++
		if text == "b" {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return floatResponse(testVectors(1)), nil
	})
	_, err := p.Generate(context.Background(), models.CohereEmbedV4, logicalRequest("a", "b", "c"))
	var handled *provider.RetriesHandledError
	if !errors.As(err, &handled) || provider.Kind(err) != provider.ErrorUnavailable {
		t.Fatalf("lost classification/retry marker: %v", err)
	}
	if !reflect.DeepEqual(counts, map[string]int{"a": 1, "b": 4}) {
		t.Fatalf("repeated success or continued after failure: %v", counts)
	}
}

func TestInvalidChunkIsNotRetried(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{})
	calls := 0
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) { calls++; return floatResponse([][]float64{{1}}), nil })
	if _, err := p.Generate(context.Background(), models.CohereEmbedV4, logicalRequest("a")); provider.Kind(err) != provider.ErrorInvalidResponse || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
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

func TestCanceledQueueDoesNotSendAndSlotIsReleased(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1, MaxConcurrency: 1})
	occupiedCtx, stopOccupied := context.WithCancel(context.Background())
	defer stopOccupied()
	started := make(chan struct{}, 4)
	var calls atomic.Int64
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	done := make(chan error, 1)
	go func() {
		_, err := p.Generate(occupiedCtx, models.CohereEmbedV4, textRequest("", "occupy"))
		done <- err
	}()
	waitStarted(t, started)
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { _, err := p.Generate(queuedCtx, models.CohereEmbedV4, logicalRequest("a", "b")); queued <- err }()
	cancelQueued()
	if err := <-queued; !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("queue cancellation dispatched HTTP: %v calls=%d", err, calls.Load())
	}
	stopOccupied()
	<-done
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) { return floatResponse(testVectors(1)), nil })
	if _, err := p.Generate(context.Background(), models.CohereEmbedV4, logicalRequest("next")); err != nil {
		t.Fatalf("slot leaked: %v", err)
	}
}

func TestBatchDeadlineCancelsInFlightAndStopsRemainingChunks(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{MaxBatchSize: 1, MaxConcurrency: 1})
	var calls atomic.Int64
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := p.Generate(ctx, models.CohereEmbedV4, logicalRequest("a", "b", "c")); !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("deadline/remaining chunks: %v calls=%d", err, calls.Load())
	}
	if len(p.slots) != 0 {
		t.Fatal("deadline leaked slot")
	}
}
