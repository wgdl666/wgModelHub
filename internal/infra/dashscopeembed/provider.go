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
		client:   &http.Client{Timeout: 20 * time.Second},
		endpoint: baseURL + "/services/embeddings/multimodal-embedding/multimodal-embedding",
		apiKey:   apiKey,
	}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.Qwen3VLEmbedding {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "dashscope embedding does not serve model %q", model)
	}
	content, err := embedContent(request)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"model":      model,
		"input":      map[string]any{"contents": []map[string]string{content}},
		"parameters": map[string]any{"dimension": vectorDim, "output_type": "dense"},
	}
	vector, err := p.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	return embeddingEvent(vector)
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

func (p *Provider) post(ctx context.Context, payload any) ([]float64, error) {
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
			Embeddings []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"embeddings"`
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
	if decoded.Output == nil || len(decoded.Output.Embeddings) == 0 || len(decoded.Output.Embeddings[0].Embedding) != vectorDim {
		return nil, provider.New(provider.ErrorInvalidResponse, "dashscope embedding must return 1024 floats")
	}
	return decoded.Output.Embeddings[0].Embedding, nil
}
