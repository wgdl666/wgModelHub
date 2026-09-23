// Package cohereembed 把 Cohere 官方 embed-v4.0 收成 ModelHub 文本结果。
// query 和以 { 开头的文档文本使用不同 input_type，混用会拉低召回。
package cohereembed

import (
	"bytes"
	"context"
	"encoding/base64"
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
		return nil, provider.New(provider.ErrorConfiguration, "cohere embedding requires base_url and api_key")
	}
	return &Provider{name: name, client: &http.Client{Timeout: 20 * time.Second}, endpoint: baseURL + "/v2/embed", apiKey: apiKey}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.CohereEmbedV4 {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "cohere embedding does not serve model %q", model)
	}
	payload, err := coherePayload(model, request)
	if err != nil {
		return nil, err
	}
	vector, err := p.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"embedding": vector})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode embedding", err)
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

func coherePayload(model string, request *modelhubv2.GenerateRequest) (map[string]any, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	base := map[string]any{"model": model, "embedding_types": []string{"float"}, "output_dimension": vectorDim}
	if image := provider.FirstImageMedia(input); image != nil {
		data, ok := provider.InlineImageBytes(image)
		if !ok {
			return nil, provider.New(provider.ErrorInvalidArgument, "embedding image must be inline bytes")
		}
		mime := image.GetMimeType()
		if mime == "" {
			mime = "image/jpeg"
		}
		base["input_type"] = "search_document"
		base["images"] = []string{"data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}
		return base, nil
	}
	text := strings.TrimSpace(provider.UserText(input))
	if text == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "embedding requires an image or text")
	}
	inputType := "search_query"
	if strings.HasPrefix(text, "{") {
		inputType = "search_document"
	}
	base["input_type"] = inputType
	base["texts"] = []string{text}
	return base, nil
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
	vector, err := decodeFloatEmbedding(raw)
	if err != nil {
		return nil, err
	}
	if len(vector) != vectorDim {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere embedding dimension is %d, want %d", len(vector), vectorDim)
	}
	return vector, nil
}

func decodeFloatEmbedding(raw []byte) ([]float64, error) {
	var decoded struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere embedding", err)
	}
	if len(decoded.Embeddings) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response is empty")
	}
	if bytes.HasPrefix(bytes.TrimSpace(decoded.Embeddings), []byte("[")) {
		var rows [][]float64
		if err := json.Unmarshal(decoded.Embeddings, &rows); err != nil || len(rows) == 0 {
			return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response is empty")
		}
		return rows[0], nil
	}
	var byType struct {
		Float [][]float64 `json:"float"`
	}
	if err := json.Unmarshal(decoded.Embeddings, &byType); err != nil || len(byType.Float) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "cohere embedding response is empty")
	}
	return byType.Float[0], nil
}
