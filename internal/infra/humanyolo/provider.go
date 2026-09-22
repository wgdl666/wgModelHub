// Package humanyolo 把自建人体检测收成 ModelHub 文本能力。
// 衣橱只传一张图；置信度、IoU、地址和 Basic Auth 属于这个供应商，不进 RPC。
package humanyolo

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
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
	confidence  = "0.35"
	iou         = "0.45"
	timeout     = 60 * time.Second
)

// Provider 只实现 TextProvider，返回上游 JSON，裁图留在衣橱。
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
		return nil, provider.New(provider.ErrorConfiguration, "human-yolo name and base_url are required")
	}
	if username == "" || password == "" {
		return nil, provider.New(provider.ErrorConfiguration, "human-yolo username and password are required")
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
	if model != models.HumanYOLO {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "human-yolo does not serve model %q", model)
	}
	imageBytes, err := oneInlineImage(request.GetInput())
	if err != nil {
		return nil, err
	}
	body, err := p.post(ctx, imageBytes)
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

func (p *Provider) post(ctx context.Context, imageBytes []byte) ([]byte, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "person.png")
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" build yolo request", err)
	}
	if _, err := file.Write(imageBytes); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" write yolo image", err)
	}
	if err := writer.WriteField("conf", confidence); err != nil || writer.WriteField("iou", iou) != nil || writer.Close() != nil {
		return nil, provider.New(provider.ErrorUnavailable, p.name+" finish yolo request")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+predictPath, &body)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" create yolo request", err)
	}
	httpReq.Header.Set("content-type", writer.FormDataContentType())
	httpReq.SetBasicAuth(p.username, p.password)
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
		return nil, provider.New(provider.ErrorInvalidArgument, "human-yolo requires exactly one input image")
	}
	data, ok := images[0].Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return nil, provider.New(provider.ErrorInvalidArgument, "human-yolo requires inline image bytes")
	}
	return data.Data, nil
}
