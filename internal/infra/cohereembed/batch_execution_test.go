package cohereembed

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
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
func TestBatchForwardsWholeRequestInOrder(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{})
	var sizes []int
	p.client = embeddingDoer(func(r *http.Request) (*http.Response, error) {
		texts := payloadTexts(r)
		sizes = append(sizes, len(texts))
		rows := testVectors(len(texts))
		for i, text := range texts {
			n, _ := strconv.Atoi(text)
			rows[i][0] = float64(n)
		}
		return floatResponse(rows), nil
	})
	texts := make([]string, 97)
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
	if len(decoded.Embeddings) != len(texts) {
		t.Fatalf("rows=%d", len(decoded.Embeddings))
	}
	for i, row := range decoded.Embeddings {
		if row[0] != float64(i) {
			t.Fatalf("order changed at %d", i)
		}
	}
	if !reflect.DeepEqual(sizes, []int{97}) {
		t.Fatalf("upstream request sizes=%v", sizes)
	}
}

// 不同调用方请求互不占共享名额，普通文本和录衣图片不受批量功能额外限流。
func TestIndependentRequestsHaveNoSharedConcurrencyGate(t *testing.T) {
	p := mustProvider(t, config.CohereEmbeddingProviderConfig{})
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
		count := len(payloadTexts(r))
		if count == 0 {
			count = 1
		}
		return floatResponse(testVectors(count)), nil
	})
	requests := []*modelhubv2.GenerateRequest{logicalRequest("a", "b"), logicalRequest("coat"), logicalRequest("pants"), imageRequest()}
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
	close(release)
	for range requests {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != int64(len(requests)) {
		t.Fatalf("calls=%d", calls.Load())
	}
}
