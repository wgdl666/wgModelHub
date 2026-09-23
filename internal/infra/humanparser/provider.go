// Package humanparser 把自建人体解析收成 ModelHub 文本能力。
// 返回上游 JSON；单件提取仍由衣橱根据分割图完成。
package humanparser

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
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	predictPath = "/predict"
	timeout     = 120 * time.Second
)

// Provider 只实现 TextProvider。账号密码成对出现，两个都空则匿名调用。
type Provider struct {
	name     string
	baseURL  string
	username string
	password string
	client   *http.Client
}

func New(name, baseURL, username, password string) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if name == "" || baseURL == "" {
		return nil, provider.New(provider.ErrorConfiguration, "human-parser name and base_url are required")
	}
	if (username == "") != (password == "") {
		return nil, provider.New(provider.ErrorConfiguration, "human-parser username and password must be configured together")
	}
	client := telemetry.NewHTTPClient()
	client.Timeout = timeout
	return &Provider{
		name:     name,
		baseURL:  baseURL,
		username: username,
		password: password,
		client:   client,
	}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.HumanParser {
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "human-parser does not serve model %q", model)
	}
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 1 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "human-parser requires exactly one input image")
	}
	data, ok := images[0].Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "human-parser requires inline image bytes")
	}
	payload, err := json.Marshal(map[string]string{"image": base64.StdEncoding.EncodeToString(data.Data)})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode parser image", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+predictPath, bytes.NewReader(payload))
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" create parser request", err)
	}
	httpReq.Header.Set("content-type", "application/json")
	if p.username != "" {
		httpReq.SetBasicAuth(p.username, p.password)
	}
	response, err := p.client.Do(httpReq)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" parser request failed", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read parser response", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.FromHTTPDetail(p.name, response.StatusCode, string(body))
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	text := ""
	if len(event.GetItems()) > 0 {
		text = event.GetItems()[0].GetText()
	}
	if emit != nil && text != "" {
		if err := emit(provider.TextDeltaEvent(text)); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}
