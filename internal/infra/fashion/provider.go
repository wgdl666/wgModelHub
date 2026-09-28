// Package fashion 保持已有服装 embedding / rerank 的输入、维度和结果契约，禁止替换向量模型。
package fashion

import (
	"bytes"
	"context"
	"encoding/json"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Provider struct {
	name, baseURL, username, password string
	client                            *http.Client
}

func New(name, baseURL, username, password string) (*Provider, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, provider.New(provider.ErrorConfiguration, "fashion requires HTTP base_url")
	}
	return &Provider{name, strings.TrimRight(baseURL, "/"), username, password, telemetry.NewHTTPClient()}, nil
}
func (p *Provider) Generate(ctx context.Context, model string, in *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	endpoint := ""
	switch model {
	case "fashion-embedding":
		endpoint = "/v1/embed"
	case "fashion-reranker":
		endpoint = "/v1/rank"
	default:
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "unsupported fashion model")
	}
	raw := []byte(provider.UserText(in.GetInput()))
	if !json.Valid(raw) {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "fashion input requires JSON")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.username != "" {
		req.SetBasicAuth(p.username, p.password)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "fashion request", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "fashion response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, provider.Errorf(provider.ErrorUnavailable, "fashion HTTP %d", resp.StatusCode)
	}
	if !json.Valid(body) {
		return nil, provider.New(provider.ErrorUnavailable, "fashion response is not JSON")
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}
func (p *Provider) GenerateStream(ctx context.Context, model string, in *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, in)
	if err != nil {
		return nil, err
	}
	if err = emit(provider.TextDeltaEvent(event.Items[0].GetText())); err != nil {
		return nil, err
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}
