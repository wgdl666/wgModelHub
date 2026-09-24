// Package coherererank 把 Cohere 官网 rerank-v3.5 收成 ModelHub 文本结果。
// 该接口没有 instruct，调用方带上的指令不进入请求。
package coherererank

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

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type rerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type scored struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
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
		return nil, provider.New(provider.ErrorConfiguration, "cohere rerank requires base_url and api_key")
	}
	return &Provider{name: name, client: &http.Client{Timeout: 20 * time.Second}, endpoint: baseURL + "/v2/rerank", apiKey: apiKey}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.CohereRerankV35Official {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "cohere rerank does not serve model %q", model)
	}
	parsed, err := parseRerank(request)
	if err != nil {
		return nil, err
	}
	raw, err := p.post(ctx, map[string]any{
		"model":     model,
		"query":     parsed.Query,
		"documents": parsed.Documents,
		"top_n":     len(parsed.Documents),
	})
	if err != nil {
		return nil, err
	}
	results, err := decodeResults(raw, len(parsed.Documents))
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode rerank", err)
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

func parseRerank(request *modelhubv2.GenerateRequest) (rerankRequest, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	var parsed rerankRequest
	if err := json.Unmarshal([]byte(provider.UserText(input)), &parsed); err != nil {
		return rerankRequest{}, provider.New(provider.ErrorInvalidArgument, "rerank requires JSON query and documents")
	}
	if strings.TrimSpace(parsed.Query) == "" || len(parsed.Documents) == 0 || len(parsed.Documents) > 100 {
		return rerankRequest{}, provider.New(provider.ErrorInvalidArgument, "rerank query and 1 to 100 documents are required")
	}
	for _, document := range parsed.Documents {
		if strings.TrimSpace(document) == "" {
			return rerankRequest{}, provider.New(provider.ErrorInvalidArgument, "rerank document text is required")
		}
	}
	return parsed, nil
}

func (p *Provider) post(ctx context.Context, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode rerank", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" create rerank request", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" rerank", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read rerank", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, provider.Errorf(provider.ErrorUnavailable, "%s rerank HTTP %d", p.name, resp.StatusCode)
	}
	return raw, nil
}

// decodeResults 要求每件文档都有分。切断按下标对回候选，缺一件就不能用这批结果。
func decodeResults(raw []byte, want int) ([]scored, error) {
	var decoded struct {
		Results []scored `json:"results"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode cohere rerank", err)
	}
	if len(decoded.Results) != want {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere rerank returned %d results, want %d", len(decoded.Results), want)
	}
	seen := make(map[int]struct{}, want)
	for _, item := range decoded.Results {
		if item.Index < 0 || item.Index >= want {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere rerank index %d out of range", item.Index)
		}
		if _, ok := seen[item.Index]; ok {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "cohere rerank duplicate index %d", item.Index)
		}
		seen[item.Index] = struct{}{}
	}
	return decoded.Results, nil
}
