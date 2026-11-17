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

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const vectorDim = provider.EmbeddingDimension

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Provider 只实现 TextProvider。有图走图片，否则走文本。
type Provider struct {
	name     string
	client   httpDoer
	endpoint string
	apiKey   string
}

func New(name, baseURL, apiKey string) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	if name == "" || baseURL == "" || apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, "dashscope embedding requires base_url and api_key")
	}
	return &Provider{
		name:     name,
		client:   telemetry.NewTimedHTTPClient(20 * time.Second),
		endpoint: baseURL + "/services/embeddings/multimodal-embedding/multimodal-embedding",
		apiKey:   apiKey,
	}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.Qwen3VLEmbedding {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "dashscope embedding does not serve model %q", model)
	}
	input, err := provider.ParseEmbeddingInput(request.GetInput())
	if err != nil {
		return nil, err
	}
	var contents []map[string]string
	if input.ImageDataURI != "" {
		contents = []map[string]string{{"image": input.ImageDataURI}}
	} else {
		contents = make([]map[string]string, len(input.Texts))
		for i, text := range input.Texts {
			contents[i] = map[string]string{"text": text}
		}
	}
	rows, err := p.post(ctx, model, contents)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(contents) {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "dashscope embedding count is %d, want %d", len(rows), len(contents))
	}
	vectors := make([][]float64, len(rows))
	for _, row := range rows {
		index := 0
		// 多条响应按分片内 index 对齐输入，单条只有一个位置。
		if len(rows) > 1 {
			if row.Index == nil || *row.Index < 0 || *row.Index >= len(vectors) || vectors[*row.Index] != nil {
				return nil, provider.New(provider.ErrorInvalidResponse, "dashscope embedding returned an invalid or duplicate index")
			}
			index = *row.Index
		}
		if len(row.Embedding) != vectorDim {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "dashscope embedding dimension is %d, want %d", len(row.Embedding), vectorDim)
		}
		vectors[index] = row.Embedding
	}
	return provider.EmbeddingEvent(vectors)
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	return emitText(event, emit)
}

type embeddingRow struct {
	Index     *int      `json:"index"`
	Embedding []float64 `json:"embedding"`
}

func (p *Provider) post(ctx context.Context, model string, contents []map[string]string) ([]embeddingRow, error) {
	// 每个 contents 条目独立计算一个向量。
	parameters := map[string]any{"dimension": vectorDim, "output_type": "dense", "enable_fusion": false}
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
