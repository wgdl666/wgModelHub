// Package dashscopeembed 把百炼多模态 embedding 收成 ModelHub 文本结果。
// 图片和文本都固定 1024 维，避免两条通路悄悄换向量空间。
package dashscopeembed

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const vectorDim = 1024

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Provider 只实现 TextProvider。有图走图片，否则走文本。
type Provider struct {
	name     string
	client   httpDoer
	endpoint string
	apiKey   string
	limits   config.DashScopeEmbeddingProviderConfig
}

func New(name string, cfg *config.DashScopeEmbeddingProviderConfig) (*Provider, error) {
	if cfg == nil {
		return nil, provider.New(provider.ErrorConfiguration, "dashscope embedding config is required")
	}
	limits := cfg.Resolved()
	if err := limits.ValidateExecutionLimits(); err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "dashscope embedding execution limits", err)
	}
	name = strings.TrimSpace(name)
	baseURL := strings.TrimRight(strings.TrimSpace(limits.BaseURL), "/")
	apiKey := strings.TrimSpace(limits.APIKey)
	if name == "" || baseURL == "" || apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, "dashscope embedding requires base_url and api_key")
	}
	return &Provider{
		name:     name,
		client:   telemetry.NewTimedHTTPClient(20 * time.Second),
		endpoint: baseURL + "/services/embeddings/multimodal-embedding/multimodal-embedding",
		apiKey:   apiKey,
		limits:   limits,
	}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.Qwen3VLEmbedding {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "dashscope embedding does not serve model %q", model)
	}
	texts, err := provider.EmbeddingBatchTexts(request.GetInput(), p.limits.MaxRequestTexts, p.limits.MaxRequestTextBytes)
	if err != nil {
		return nil, err
	}
	if texts != nil {
		return p.embedBatch(ctx, model, texts)
	}
	content, err := embedContent(request)
	if err != nil {
		return nil, err
	}
	rows, err := p.post(ctx, model, []map[string]string{content}, false)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || len(rows[0].Embedding) != vectorDim {
		return nil, provider.New(provider.ErrorInvalidResponse, "dashscope embedding must return 1024 floats")
	}
	return embeddingEvent("embedding", rows[0].Embedding)
}

func (p *Provider) embedBatch(ctx context.Context, model string, texts []string) (*modelhubv2.GenerateEvent, error) {
	ctx, span := logs.StartSpan(ctx, "dashscope.embedding.batch_request")
	defer span.End()
	batchSize := p.limits.MaxBatchSize
	span.SetInt("embedding.text_count", len(texts))
	span.SetInt("embedding.batch_count", (len(texts)+batchSize-1)/batchSize)

	vectors := make([][]float64, len(texts))
	for start := 0; start < len(texts); start += batchSize {
		if err := ctx.Err(); err != nil {
			if start == 0 {
				return nil, provider.WrapNotAttempted(provider.ErrorTimeout, "dashscope embedding canceled before request", err)
			}
			return nil, provider.Wrap(provider.ErrorTimeout, "dashscope embedding canceled between batches", err)
		}
		end := min(start+batchSize, len(texts))
		contents := make([]map[string]string, 0, end-start)
		for _, text := range texts[start:end] {
			contents = append(contents, map[string]string{"text": text})
		}
		rows, err := p.post(ctx, model, contents, true)
		if err != nil {
			return nil, err
		}
		if len(rows) != len(contents) {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "dashscope embedding count is %d, want %d", len(rows), len(contents))
		}
		// 百炼的 index 是当前分片的位置，不能依赖响应数组顺序。
		for _, row := range rows {
			if row.Index == nil || *row.Index < 0 || *row.Index >= len(contents) || vectors[start+*row.Index] != nil {
				return nil, provider.New(provider.ErrorInvalidResponse, "dashscope embedding returned an invalid or duplicate index")
			}
			if len(row.Embedding) != vectorDim {
				return nil, provider.Errorf(provider.ErrorInvalidResponse, "dashscope embedding dimension is %d, want %d", len(row.Embedding), vectorDim)
			}
			vectors[start+*row.Index] = row.Embedding
		}
	}
	return embeddingEvent("embeddings", vectors)
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	return emitText(event, emit)
}

func embedContent(request *modelhubv2.GenerateRequest) (map[string]string, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	if image := provider.FirstImageMedia(input); image != nil {
		data, ok := provider.InlineImageBytes(image)
		if !ok {
			return nil, provider.New(provider.ErrorInvalidArgument, "embedding image must be inline bytes")
		}
		return map[string]string{"image": "data:" + imageMime(image) + ";base64," + encodeBase64(data)}, nil
	}
	text := strings.TrimSpace(provider.UserText(input))
	if text == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "embedding requires an image or text")
	}
	return map[string]string{"text": text}, nil
}

type embeddingRow struct {
	Index     *int      `json:"index"`
	Embedding []float64 `json:"embedding"`
}

func (p *Provider) post(ctx context.Context, model string, contents []map[string]string, batch bool) ([]embeddingRow, error) {
	parameters := map[string]any{"dimension": vectorDim, "output_type": "dense"}
	if batch {
		parameters["enable_fusion"] = false
	}
	payload := map[string]any{
		"model":      model,
		"input":      map[string]any{"contents": contents},
		"parameters": parameters,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode embedding", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" create embedding request", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" embedding", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read embedding", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, provider.Errorf(provider.ErrorUnavailable, "%s embedding HTTP %d", p.name, resp.StatusCode)
	}
	var decoded struct {
		Output *struct {
			Embeddings []embeddingRow `json:"embeddings"`
		} `json:"output"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode embedding", err)
	}
	if decoded.Code != "" {
		return nil, provider.Errorf(provider.ErrorUnavailable, "%s embedding %s", p.name, decoded.Code)
	}
	if decoded.Output == nil {
		return nil, provider.New(provider.ErrorInvalidResponse, "dashscope embedding response has no output")
	}
	return decoded.Output.Embeddings, nil
}
