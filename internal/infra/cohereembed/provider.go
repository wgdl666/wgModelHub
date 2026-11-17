// Package cohereembed 把 Cohere 官方 embed-v4.0 收成 ModelHub 文本结果。
// 查询和建索引使用不同的 input_type。
package cohereembed

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	vectorDim        = provider.EmbeddingDimension
	maxResponseBytes = 4 << 20

	// 2026-11 美东路演 7 天成功调用 P95 约 1.7 秒、P99 约 3.6 秒，原先单次等满 20 秒才失败。
	attemptTimeout = 3 * time.Second
	// 只多发一次：供应商整体变慢时再发也救不回，剩下的交给调用方的业务重试。
	maxAttempts = 2
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Provider struct {
	name     string
	client   httpDoer
	endpoint string
	apiKey   string
	// attemptTimeout 是单次 HTTP 从发出到读完正文的上限；调用方 ctx 更早结束时以 ctx 为准。
	attemptTimeout time.Duration
}

func New(name string, cfg *config.CohereEmbeddingProviderConfig) (*Provider, error) {
	if cfg == nil {
		return nil, provider.New(provider.ErrorConfiguration, "cohere embedding config is required")
	}
	name = strings.TrimSpace(name)
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	apiKey := strings.TrimSpace(cfg.APIKey)
	if name == "" || baseURL == "" || apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, "cohere embedding requires base_url and api_key")
	}
	// 请求并发与总 deadline 仍由调用方管理；单次 HTTP 上限由 post 按次设置，客户端自身不再钉总超时。
	return &Provider{
		name:           name,
		client:         telemetry.NewHTTPClient(),
		endpoint:       baseURL + "/v2/embed",
		apiKey:         apiKey,
		attemptTimeout: attemptTimeout,
	}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.CohereEmbedV4 {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "cohere embedding does not serve model %q", model)
	}
	input, err := provider.ParseEmbeddingInput(request.GetInput())
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"model": model, "input_type": input.SearchType(),
		"embedding_types": []string{"float"}, "output_dimension": vectorDim,
	}
	expected := len(input.Texts)
	if input.ImageDataURI != "" {
		payload["images"] = []string{input.ImageDataURI}
		expected = 1
	} else {
		payload["texts"] = input.Texts
	}
	vectors, err := p.post(ctx, payload, expected)
	if err != nil {
		return nil, err
	}
	return provider.EmbeddingEvent(vectors)
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

// post 发送一个 HTTP 请求体并校验向量。单次等满 attemptTimeout 仍没有完整响应时立即重发一次：向量计算幂等，重发没有业务副作用。
// 限流、5xx 和连接错误不在这里重发，它们已由 Service 的同步文本重试覆盖；那一层不重试超时，所以超时只在这里处理。
func (p *Provider) post(ctx context.Context, payload any, expected int) ([][]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorTimeout, "cohere embedding canceled before request", err)
	}
	body, err := httpbody.JSON(payload, true)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}

	var raw []byte
	for attempt := 1; ; attempt++ {
		started := time.Now()
		raw, err = p.postOnce(ctx, body)
		if err == nil {
			break
		}
		// 只重发“这一次自己超时”的请求；调用方已取消或到期时原样返回。
		attemptTimedOut := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
		if !attemptTimedOut || attempt >= maxAttempts {
			return nil, err
		}
		logs.Default().WarnContext(ctx, "modelhub_cohere_embedding_retry", "provider", p.name, "attempt", attempt, "elapsed_ms", time.Since(started).Milliseconds())
	}

	// 批量响应可超过原先的 1 MiB；仍保留总大小上限，禁止截断后误用首行。
	if len(raw) > maxResponseBytes {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response exceeds size limit")
	}
	return provider.DecodeFloatEmbeddings(raw, expected)
}

// postOnce 发一次请求并读完正文，整个过程受 attemptTimeout 约束。
func (p *Provider) postOnce(ctx context.Context, body *httpbody.Body) ([]byte, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, p.attemptTimeout)
	defer cancel()

	req, err := body.NewRequest(attemptCtx, http.MethodPost, p.endpoint)
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
	return raw, nil
}
