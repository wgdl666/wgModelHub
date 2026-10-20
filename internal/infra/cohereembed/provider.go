// Package cohereembed 把 Cohere 官方 embed-v4.0 收成 ModelHub 文本结果。
// query 和以 { 开头的文档文本使用不同 input_type，混用会拉低召回。
package cohereembed

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	vectorDim                = 1024
	maxResponseBytes         = 4 << 20
	maxCombinedResponseBytes = 32 << 20
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider struct {
	name       string
	client     httpDoer
	endpoint   string
	apiKey     string
	limits     config.CohereEmbeddingProviderConfig
	slots      chan struct{}
	retryDelay time.Duration
}

func New(name string, cfg *config.CohereEmbeddingProviderConfig) (*Provider, error) {
	if cfg == nil {
		return nil, provider.New(provider.ErrorConfiguration, "cohere embedding config is required")
	}
	resolved := cfg.Resolved()
	name = strings.TrimSpace(name)
	baseURL := strings.TrimRight(strings.TrimSpace(resolved.BaseURL), "/")
	apiKey := strings.TrimSpace(resolved.APIKey)
	if name == "" || baseURL == "" || apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, "cohere embedding requires base_url and api_key")
	}
	if err := resolved.ValidateExecutionLimits(); err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "cohere embedding execution limits", err)
	}
	// 并发池跟随 Provider 实例共享，单条录衣和批量查询都在实际 HTTP 边界取名额。
	return &Provider{name: name, client: telemetry.NewTimedHTTPClient(20 * time.Second), endpoint: baseURL + "/v2/embed", apiKey: apiKey, limits: resolved, slots: make(chan struct{}, resolved.MaxConcurrency), retryDelay: 300 * time.Millisecond}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.CohereEmbedV4 {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "cohere embedding does not serve model %q", model)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.limits.RequestTimeoutSeconds)*time.Second)
	defer cancel()
	payload, batch, err := p.coherePayload(model, request)
	if err != nil {
		return nil, err
	}
	var vectors [][]float64
	if batch {
		vectors, err = p.embedBatch(ctx, payload)
	} else {
		vectors, err = p.post(ctx, payload, 1)
	}
	if err != nil {
		return nil, err
	}
	result := map[string]any{"embeddings": vectors}
	if !batch {
		result = map[string]any{"embedding": vectors[0]}
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}
	if len(body) > maxCombinedResponseBytes {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding combined response exceeds size limit")
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	text := event.GetItems()[0].GetText()
	if emit != nil && text != "" {
		if err := emit(provider.TextDeltaEvent(text)); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}

func (p *Provider) coherePayload(model string, request *modelhubv2.GenerateRequest) (map[string]any, bool, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	base := map[string]any{"model": model, "embedding_types": []string{"float"}, "output_dimension": vectorDim}
	// 显式标记才按条目拆批；普通多段文本仍按原协议合成一个向量。
	var traits struct {
		Batch     bool   `json:"embedding_batch"`
		InputType string `json:"input_type"`
	}
	if raw := strings.TrimSpace(input.GetModelTraits()); raw != "" {
		if err := json.Unmarshal([]byte(raw), &traits); err != nil || raw == "null" {
			return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "invalid embedding model_traits")
		}
	}
	if traits.Batch {
		if traits.InputType != "search_query" {
			return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "batch embedding requires input_type search_query")
		}
		if len(input.GetItems()) == 0 || len(input.GetItems()) > p.limits.MaxRequestTexts {
			return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "batch embedding exceeds request text count budget")
		}
		texts := make([]string, 0, len(input.GetItems()))
		textBytes := 0
		for _, item := range input.GetItems() {
			message := item.GetMessage()
			if message.GetRole() != modelhubv2.Role_ROLE_USER || len(message.GetParts()) != 1 || len(message.GetToolCalls()) != 0 {
				return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "batch embedding requires one user text per item")
			}
			text := strings.TrimSpace(message.GetParts()[0].GetText())
			if text == "" {
				return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "batch embedding requires non-empty text parts")
			}
			textBytes += len(message.GetParts()[0].GetText())
			if textBytes > p.limits.MaxRequestTextBytes {
				return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "batch embedding exceeds request text byte budget")
			}
			texts = append(texts, text)
		}
		base["input_type"] = "search_query"
		base["texts"] = texts
		return base, true, nil
	}
	if image := provider.FirstImageMedia(input); image != nil {
		data, ok := provider.InlineImageBytes(image)
		if !ok {
			return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "embedding image must be inline bytes")
		}
		mime := image.GetMimeType()
		if mime == "" {
			mime = "image/jpeg"
		}
		base["input_type"] = "search_document"
		base["images"] = []string{"data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}
		return base, false, nil
	}
	text := strings.TrimSpace(provider.UserText(input))
	if text == "" {
		return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "embedding requires an image or text")
	}
	if len(text) > p.limits.MaxRequestTextBytes {
		return nil, false, provider.NotAttempted(provider.ErrorInvalidArgument, "embedding exceeds request text byte budget")
	}
	inputType := "search_query"
	if strings.HasPrefix(text, "{") {
		inputType = "search_document"
	}
	base["input_type"] = inputType
	base["texts"] = []string{text}
	return base, false, nil
}

func (p *Provider) post(ctx context.Context, payload any, expected int) ([][]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorTimeout, "cohere embedding canceled before request", err)
	}
	waitCtx, span := logs.StartSpan(ctx, "cohere.embedding.upstream")
	defer span.End()
	waitStarted := time.Now()
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, provider.WrapNotAttempted(provider.ErrorTimeout, "cohere embedding queue canceled", ctx.Err())
	}
	defer func() { <-p.slots }()
	span.SetInt("embedding.queue_wait_ms", int(time.Since(waitStarted).Milliseconds()))
	if err := ctx.Err(); err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorTimeout, "cohere embedding canceled before request", err)
	}
	ctx = waitCtx
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorUnavailable, p.name+" create embedding request", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" embedding", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read embedding", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	// 批量响应可超过原先的 1 MiB；仍保留总大小上限，禁止截断后误用首行。
	if len(raw) > maxResponseBytes {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response exceeds size limit")
	}
	vectors, err := decodeFloatEmbeddings(raw)
	if err != nil {
		return nil, err
	}
	if len(vectors) != expected {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere embedding count is %d, want %d", len(vectors), expected)
	}
	for _, vector := range vectors {
		if len(vector) != vectorDim {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere embedding dimension is %d, want %d", len(vector), vectorDim)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding contains non-finite values")
			}
		}
	}
	return vectors, nil
}

func decodeFloatEmbeddings(raw []byte) ([][]float64, error) {
	var decoded struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere embedding", err)
	}
	if len(decoded.Embeddings) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response is empty")
	}
	rowsJSON := decoded.Embeddings
	if !bytes.HasPrefix(bytes.TrimSpace(rowsJSON), []byte("[")) {
		var byType struct {
			Float json.RawMessage `json:"float"`
		}
		if err := json.Unmarshal(rowsJSON, &byType); err != nil {
			return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere float embeddings", err)
		}
		rowsJSON = byType.Float
	}
	// encoding/json 会把 null 数值静默变成 0，不能把供应商缺失值当作有效向量。
	if bytes.Contains(rowsJSON, []byte("null")) {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding contains null values")
	}
	var rows [][]float64
	if err := json.Unmarshal(rowsJSON, &rows); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere float embeddings", err)
	}
	if len(rows) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response is empty")
	}
	return rows, nil
}

// embedBatch 在本实例共享并发池下执行逻辑请求；结果按输入位置写回，失败则整单不返回部分向量。
func (p *Provider) embedBatch(ctx context.Context, payload map[string]any) ([][]float64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	texts := payload["texts"].([]string)
	batchCount := (len(texts) + p.limits.MaxBatchSize - 1) / p.limits.MaxBatchSize
	ctx, span := logs.StartSpan(ctx, "cohere.embedding.batch_request")
	defer span.End()
	span.SetInt("embedding.text_count", len(texts))
	span.SetInt("embedding.batch_count", batchCount)
	span.SetInt("embedding.max_concurrency", p.limits.MaxConcurrency)
	rows := make([][]float64, len(texts))
	var next atomic.Int64
	var attempted atomic.Int64
	var wg sync.WaitGroup
	var firstErr error
	var failOnce sync.Once
	for worker := 0; worker < min(batchCount, p.limits.MaxConcurrency); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil {
					return
				}
				index := int(next.Add(1) - 1)
				if index >= batchCount {
					return
				}
				start := index * p.limits.MaxBatchSize
				end := min(start+p.limits.MaxBatchSize, len(texts))
				batchCtx, batchSpan := logs.StartSpan(ctx, "cohere.embedding.batch")
				batchSpan.SetInt("embedding.batch_index", index)
				batchSpan.SetInt("embedding.text_count", end-start)
				part := map[string]any{"model": payload["model"], "embedding_types": payload["embedding_types"], "output_dimension": vectorDim, "input_type": "search_query", "texts": texts[start:end]}
				vectors, err := p.postBatchWithRetry(batchCtx, part, end-start, &attempted)
				if err != nil {
					telemetry.RecordError(batchCtx, err)
				}
				batchSpan.End()
				if err != nil {
					failOnce.Do(func() { firstErr = err; cancel() })
					return
				}
				copy(rows[start:end], vectors)
			}
		}()
	}
	wg.Wait()
	if firstErr == nil && ctx.Err() != nil {
		firstErr = provider.WrapNotAttempted(provider.ErrorTimeout, "cohere batch embedding canceled", ctx.Err())
	}
	if firstErr != nil {
		// 某个分片已尝试调用时，不能因另一分片排队取消而把整单记成未出站。
		if attempted.Load() > 0 && provider.IsNotAttempted(firstErr) {
			firstErr = &provider.Error{Kind: provider.Kind(firstErr), Message: "cohere batch embedding failed", Err: firstErr}
		}
		telemetry.RecordError(ctx, firstErr)
		return nil, &provider.RetriesHandledError{Err: firstErr}
	}
	return rows, nil
}

// postBatchWithRetry 只重试当前分片，退避期间归还共享名额；禁止外层重算已成功分片。
func (p *Provider) postBatchWithRetry(ctx context.Context, payload map[string]any, expected int, attempted *atomic.Int64) ([][]float64, error) {
	for attempt := 0; ; attempt++ {
		vectors, err := p.post(ctx, payload, expected)
		if !provider.IsNotAttempted(err) {
			attempted.Add(1)
		}
		if err == nil {
			return vectors, nil
		}
		if attempt >= 3 || provider.IsNotAttempted(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		kind := provider.Kind(err)
		if kind != provider.ErrorRateLimited && kind != provider.ErrorUnavailable {
			return nil, err
		}
		logs.SpanFromContext(ctx).SetInt("embedding.retry_count", attempt+1)
		timer := time.NewTimer(time.Duration(attempt+1) * p.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, provider.Wrap(provider.ErrorTimeout, "cohere batch embedding retry canceled", ctx.Err())
		case <-timer.C:
		}
	}
}
