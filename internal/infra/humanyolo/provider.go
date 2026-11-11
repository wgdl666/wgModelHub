// Package humanyolo 把自建人体检测收成 ModelHub 文本能力。
// 衣橱只传一张图；置信度、IoU、地址和 Basic Auth 属于这个供应商，不进 RPC。
package humanyolo

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	predictPath = "/predict"
	confidence  = "0.35"
	iou         = "0.45"
	timeout     = 60 * time.Second
)

// Provider 只实现 TextProvider，返回上游 JSON，裁图留在衣橱。
type Provider struct {
	// Gateway 启用登记模型路由，直连供应商继续使用原有契约。
	Gateway  bool
	name     string
	baseURL  string
	username string
	password string
	client   *http.Client
}

func New(name, baseURL, username, password string, options ...telemetry.HTTPClientOption) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if name == "" || baseURL == "" {
		return nil, provider.New(provider.ErrorConfiguration, "human-yolo name and base_url are required")
	}
	if (username == "") != (password == "") {
		return nil, provider.New(provider.ErrorConfiguration, "human-yolo username and password must be configured together")
	}
	client := telemetry.NewHTTPClient(options...)
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
	if !p.Gateway && model != models.HumanYOLO {
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "human-yolo does not serve model %q", model)
	}
	imageBytes, err := oneInlineImage(request.GetInput())
	if err != nil {
		return nil, err
	}
	body, err := p.post(ctx, model, imageBytes)
	if err != nil {
		return nil, err
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

func (p *Provider) post(ctx context.Context, model string, imageBytes []byte) ([]byte, error) {
	body, err := httpbody.Multipart([]httpbody.Part{
		httpbody.File("file", "person.png", "application/octet-stream", imageBytes),
		httpbody.Field("conf", confidence), httpbody.Field("iou", iou),
	})
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorUnavailable, p.name+" build yolo request", err)
	}
	httpReq, err := body.NewRequest(ctx, http.MethodPost, p.baseURL+predictPath)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" create yolo request", err)
	}
	if p.username != "" {
		httpReq.SetBasicAuth(p.username, p.password)
	}
	if p.Gateway {
		httpReq.Header.Set("X-Model-Key", model)
	}
	response, err := p.client.Do(httpReq)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" yolo request failed", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read yolo response", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.FromHTTPDetail(p.name, response.StatusCode, string(payload))
	}
	return payload, nil
}

func oneInlineImage(input *modelhubv2.Input) ([]byte, error) {
	images := provider.ImageMedias(input)
	if len(images) != 1 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "human-yolo requires exactly one input image")
	}
	data, ok := images[0].Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "human-yolo requires inline image bytes")
	}
	return data.Data, nil
}
