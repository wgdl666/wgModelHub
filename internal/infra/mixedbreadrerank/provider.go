// Package mixedbreadrerank 把 Mixedbread 官网 listwise 精排收成 ModelHub 文本结果。
// 该接口没有 instruct 字段。上游可传可不传，这里都不转发；query 里的颜色词收成主色检索句。
package mixedbreadrerank

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

// maxInputDocuments 是官网 input 上限。衣橱现在按 100 切批，这里不比供应商更严。
const maxInputDocuments = 1000

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type rerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type wireRequest struct {
	Model        string   `json:"model"`
	Query        string   `json:"query"`
	Input        []string `json:"input"`
	ReturnInput  bool     `json:"return_input"`
	RewriteQuery bool     `json:"rewrite_query"`
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
		return nil, provider.New(provider.ErrorConfiguration, "mixedbread rerank requires base_url and api_key")
	}
	return &Provider{name: name, client: telemetry.NewTimedHTTPClient(20 * time.Second), endpoint: baseURL + "/v1/reranking", apiKey: apiKey}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.MixedbreadRerankV31Listwise {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "mixedbread rerank does not serve model %q", model)
	}
	parsed, err := parseRerank(request)
	if err != nil {
		return nil, err
	}
	raw, err := p.post(ctx, wireRequest{
		Model:        model,
		Query:        assembleQuery(parsed.Query),
		Input:        parsed.Documents,
		ReturnInput:  false,
		RewriteQuery: false,
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

// assembleQuery 只用 query 里的颜色词。金标测过的问法是「检索主色是「词」的单品」；不看 instruct 原文。
func assembleQuery(query string) string {
	return "检索主色是「" + strings.TrimSpace(query) + "」的单品"
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
	if strings.TrimSpace(parsed.Query) == "" || len(parsed.Documents) == 0 || len(parsed.Documents) > maxInputDocuments {
		return rerankRequest{}, provider.Errorf(provider.ErrorInvalidArgument, "rerank query and 1 to %d documents are required", maxInputDocuments)
	}
	for _, document := range parsed.Documents {
		if strings.TrimSpace(document) == "" {
			return rerankRequest{}, provider.New(provider.ErrorInvalidArgument, "rerank document text is required")
		}
	}
	return parsed, nil
}

func (p *Provider) post(ctx context.Context, payload wireRequest) ([]byte, error) {
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
		Data []struct {
			Index int     `json:"index"`
			Score float64 `json:"score"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode mixedbread rerank", err)
	}
	if len(decoded.Data) != want {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "mixedbread rerank returned %d results, want %d", len(decoded.Data), want)
	}
	seen := make(map[int]struct{}, want)
	out := make([]scored, 0, want)
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= want {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "mixedbread rerank index %d out of range", item.Index)
		}
		if _, ok := seen[item.Index]; ok {
			return nil, provider.Errorf(provider.ErrorInvalidResponse, "mixedbread rerank duplicate index %d", item.Index)
		}
		seen[item.Index] = struct{}{}
		out = append(out, scored{Index: item.Index, RelevanceScore: item.Score})
	}
	return out, nil
}
