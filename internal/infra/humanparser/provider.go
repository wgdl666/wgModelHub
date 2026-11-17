// Package humanparser 把自建人体解析收成 ModelHub 文本能力。
// 返回上游 JSON；单件提取仍由衣橱根据分割图完成。
package humanparser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const (
	timeout = 120 * time.Second
	// responseLimit 盖住 2048 边长分割图的 base64。超过说明上游没按像素上限返回。
	responseLimit = 16 << 20
)

// Provider 只转换人体解析输入和结果，连接与凭据统一由 Gateway 客户端持有。
type Provider struct {
	name    string
	gateway *vmindgateway.Client
}

func New(name string, gateway *vmindgateway.Client) *Provider {
	return &Provider{name: name, gateway: gateway}
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 1 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "human-parser requires exactly one input image")
	}
	data, ok := images[0].Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "human-parser requires inline image bytes")
	}
	input := map[string]string{"image": base64.StdEncoding.EncodeToString(data.Data)}
	response, err := p.gateway.Invoke(ctx, model, input, timeout)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" parser request failed", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read parser response", err)
	}
	if len(body) > responseLimit {
		return nil, provider.New(provider.ErrorInvalidResponse, p.name+" parser response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.FromHTTPDetail(p.name, response.StatusCode, string(body))
	}
	// G7 用 HTTP 200 包业务失败。不在这里拦住的话，账本会记成成功，衣橱才发现没有分割图。
	if err := requireParserSuccess(p.name, body); err != nil {
		return nil, err
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func requireParserSuccess(name string, body []byte) error {
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return provider.New(provider.ErrorInvalidResponse, name+" parser response is not json")
	}
	if payload.Success {
		return nil
	}
	detail := strings.TrimSpace(payload.Message)
	if detail == "" {
		detail = strings.TrimSpace(payload.Error)
	}
	if detail == "" {
		detail = "success=false"
	}
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return provider.New(provider.ErrorInvalidResponse, name+" parser rejected image: "+detail)
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
