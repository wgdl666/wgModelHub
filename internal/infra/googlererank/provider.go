// Package googlererank 把 Google Ranking API 收成现有 Generate 文本精排协议。
package googlererank

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/rerankinstruction"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const requestTimeout = 20 * time.Second

type Provider struct {
	// Instructions 由启动装配注入；连接不变，模板按请求读取。
	Instructions rerankinstruction.Resolver
	name         string
	project      string
	endpoint     string
	client       *http.Client
	tokens       auth.TokenProvider
}

func New(name, project, proxyURL string) (*Provider, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(project) == "" || url.PathEscape(project) != project {
		return nil, provider.New(provider.ErrorConfiguration, "google rerank requires name and project")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyURL != "" {
		proxy, err := url.Parse(proxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https") {
			return nil, provider.New(provider.ErrorConfiguration, "google rerank proxy_url is invalid")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := telemetry.NewHTTPClientWithTransport(transport)
	client.Timeout = requestTimeout
	// ACK 可挂载服务账号或工作负载身份配置；ADC 负责缓存和刷新 token，禁止拿 Vertex API key 冒充身份。
	// token 交换也走同一出口，否则国内只代理 Ranking 请求仍会卡在鉴权阶段。
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
		Client: client,
	})
	if err != nil {
		// 凭据解析错误可能包含 JSON/私钥片段，不把 SDK 原文写进日志或 gRPC status。
		return nil, provider.New(provider.ErrorConfiguration, "google rerank ADC credentials are unavailable or invalid")
	}
	return &Provider{
		name: name, project: project, client: client, tokens: creds.TokenProvider,
		endpoint: "https://discoveryengine.googleapis.com/v1/projects/" + project + "/locations/global/rankingConfigs/default_ranking_config:rank",
	}, nil
}

type rerankInput struct {
	Query          string   `json:"query"`
	InstructionKey string   `json:"instruction_key"`
	Instruct       string   `json:"instruct"`
	Documents      []string `json:"documents"`
}

type record struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

type result struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	// 可用模型由当前配置的 models 路由控制；原样下发请求 ID，切换版本或 fast 不应改适配器。
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	var parsed rerankInput
	// Google 无独立 instruct；新协议由共享模板生成 Query。
	if err := json.Unmarshal([]byte(provider.UserText(input)), &parsed); err != nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "google rerank requires JSON query and documents")
	}
	if strings.TrimSpace(parsed.Query) == "" || len(parsed.Documents) == 0 || len(parsed.Documents) > 1000 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "google rerank requires query and 1 to 1000 documents")
	}
	query, _, err := p.Instructions.Resolve(parsed.Query, parsed.Instruct, parsed.InstructionKey, false)
	if err != nil {
		return nil, err
	}
	parsed.Query = query
	records := make([]record, len(parsed.Documents))
	for i, document := range parsed.Documents {
		if strings.TrimSpace(document) == "" {
			return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "google rerank document text is required")
		}
		records[i] = record{ID: strconv.Itoa(i), Content: document}
	}
	body, err := json.Marshal(struct {
		Model                         string   `json:"model"`
		Query                         string   `json:"query"`
		Records                       []record `json:"records"`
		TopN                          int      `json:"topN"`
		IgnoreRecordDetailsInResponse bool     `json:"ignoreRecordDetailsInResponse"`
	}{model, parsed.Query, records, len(records), true})
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "encode google rerank request", err)
	}
	// token 交换与模型请求共用调用方的取消和总超时；ADC 的刷新错误不携带供应商正文。
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	token, err := p.tokens.Token(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, provider.WrapNotAttempted(provider.ErrorTimeout, "google rerank token acquisition canceled", ctx.Err())
		}
		return nil, provider.NotAttempted(provider.ErrorConfiguration, "google rerank could not acquire ADC access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorConfiguration, "create google rerank request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.Value)
	req.Header.Set("X-Goog-User-Project", p.project)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "google rerank request failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 上游错误正文可能回显 query 或凭据，只按状态码归一化，不透传正文。
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "read google rerank response", err)
	}
	results, err := decodeResults(raw, len(records))
	if err != nil {
		return nil, err
	}
	text, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "encode google rerank results", err)
	}
	return provider.TextFinalEvent(string(text), nil, "", "", nil), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	if emit != nil {
		if err := emit(provider.TextDeltaEvent(event.GetItems()[0].GetText())); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}

func decodeResults(raw []byte, want int) ([]result, error) {
	var decoded struct {
		Records []struct {
			ID    string   `json:"id"`
			Score *float64 `json:"score"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, provider.New(provider.ErrorInvalidResponse, "invalid google rerank response JSON")
	}
	if len(decoded.Records) != want {
		return nil, provider.New(provider.ErrorInvalidResponse, "google rerank returned incomplete results")
	}
	results := make([]result, 0, want)
	seen := make(map[string]bool, want)
	for _, item := range decoded.Records {
		index, err := strconv.Atoi(item.ID)
		// id 由输入下标生成；不能把供应商的排序位置当成候选下标，漏分/重复会把衣服对应错。
		if err != nil || index < 0 || index >= want || strconv.Itoa(index) != item.ID || seen[item.ID] || item.Score == nil || *item.Score < 0 || *item.Score > 1 {
			return nil, provider.New(provider.ErrorInvalidResponse, "google rerank returned invalid record IDs or scores")
		}
		seen[item.ID] = true
		results = append(results, result{Index: index, Score: *item.Score})
	}
	return results, nil
}
