// Package dashscopererank 把百炼精排收成 ModelHub 文本结果。
// qwen3.7-text-rerank 走兼容 /reranks；qwen3-vl-rerank 只换原生 text-rerank 路径。
package dashscopererank

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const nativePath = "/api/v1/services/rerank/text-rerank/text-rerank"

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type rerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	Instruct  string   `json:"instruct"`
}

type Provider struct {
	name    string
	client  httpDoer
	baseURL string
	apiKey  string
}

func New(name, baseURL, apiKey string) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	if name == "" || baseURL == "" || apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, "dashscope rerank requires base_url and api_key")
	}
	return &Provider{name: name, client: &http.Client{Timeout: 20 * time.Second}, baseURL: baseURL, apiKey: apiKey}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.Qwen37TextRerank && model != models.Qwen3VLRerank {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "dashscope rerank does not serve model %q", model)
	}
	parsed, err := parseRerank(request)
	if err != nil {
		return nil, err
	}
	payload, endpoint := p.payload(model, parsed)
	raw, err := p.post(ctx, endpoint, payload)
	if err != nil {
		return nil, err
	}
	results, err := decodeResults(model == models.Qwen3VLRerank, raw)
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

func (p *Provider) payload(model string, parsed rerankRequest) (any, string) {
	if model == models.Qwen3VLRerank {
		docs := make([]map[string]string, 0, len(parsed.Documents))
		for _, text := range parsed.Documents {
			docs = append(docs, map[string]string{"text": text})
		}
		parsedURL, _ := url.Parse(p.baseURL)
		return map[string]any{
			"model": model,
			"input": map[string]any{
				"query":     map[string]string{"text": parsed.Query},
				"documents": docs,
			},
			"parameters": map[string]any{"top_n": len(parsed.Documents), "return_documents": false, "instruct": parsed.Instruct},
		}, parsedURL.Scheme + "://" + parsedURL.Host + nativePath
	}
	return map[string]any{
		"model": model, "query": parsed.Query, "documents": parsed.Documents,
		"top_n": len(parsed.Documents), "instruct": parsed.Instruct,
	}, p.baseURL + "/reranks"
}

func (p *Provider) post(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode rerank", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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

type scored struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

func decodeResults(native bool, raw []byte) ([]scored, error) {
	if native {
		var decoded struct {
			Output struct {
				Results []struct {
					Index          int     `json:"index"`
					RelevanceScore float64 `json:"relevance_score"`
				} `json:"results"`
			} `json:"output"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode native rerank", err)
		}
		out := make([]scored, 0, len(decoded.Output.Results))
		for _, item := range decoded.Output.Results {
			out = append(out, scored{Index: item.Index, RelevanceScore: item.RelevanceScore})
		}
		return out, nil
	}
	var decoded struct {
		Results []scored `json:"results"`
		Code    string   `json:"code"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode rerank", err)
	}
	if decoded.Code != "" {
		return nil, provider.Errorf(provider.ErrorUnavailable, "dashscope rerank %s", decoded.Code)
	}
	return decoded.Results, nil
}
