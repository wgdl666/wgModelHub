// Package mixedbreadrerank 把 Mixedbread 官网 listwise 精排收成 ModelHub 文本结果。
// 该接口没有 instruct 字段；新协议按共享模板生成 Query，旧协议保留既有主色包装。
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
	"github.com/wgdl666/wgModelHub/internal/infra/rerankbatch"
	"github.com/wgdl666/wgModelHub/internal/infra/rerankinstruction"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

// The listwise API accepts the entire logical request within ModelHub's 1000-document limit.
const maxInputDocuments = 1000

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type wireRequest struct {
	Model        string   `json:"model"`
	Query        string   `json:"query"`
	Input        []string `json:"input"`
	ReturnInput  bool     `json:"return_input"`
	RewriteQuery bool     `json:"rewrite_query"`
}

type scored = rerankbatch.Result

type Provider struct {
	// Instructions 由启动装配注入；连接不变，模板按请求读取。
	Instructions rerankinstruction.Resolver
	name         string
	client       httpDoer
	endpoint     string
	apiKey       string
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
	parsed, err := rerankbatch.Parse(request)
	if err != nil {
		return nil, err
	}
	parsed.Query, _, err = p.Instructions.Resolve(parsed.Query, parsed.Instruct, parsed.InstructionKey, false)
	if err != nil {
		return nil, err
	}
	// 旧客户端未传 key 时保留既有主色包装；新协议已渲染，不能二次包装 look。
	if parsed.InstructionKey == "" {
		parsed.Query = assembleQuery(parsed.Query)
	}
	results, err := rerankbatch.Run(ctx, parsed.Documents, maxInputDocuments, func(batchCtx context.Context, documents []string) ([]scored, error) {
		raw, err := p.post(batchCtx, wireRequest{
			Model:        model,
			Query:        parsed.Query,
			Input:        documents,
			ReturnInput:  false,
			RewriteQuery: false,
		})
		if err != nil {
			return nil, err
		}
		return decodeResults(raw)
	})
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
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	return raw, nil
}

func decodeResults(raw []byte) ([]scored, error) {
	var decoded struct {
		Data []struct {
			Index int     `json:"index"`
			Score float64 `json:"score"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "decode mixedbread rerank", err)
	}
	out := make([]scored, 0, len(decoded.Data))
	for _, item := range decoded.Data {
		out = append(out, scored{Index: item.Index, RelevanceScore: item.Score})
	}
	return out, nil
}
