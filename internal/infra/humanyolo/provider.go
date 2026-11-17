// Package humanyolo 把自建人体检测收成 ModelHub 文本能力。
// 衣橱只传一张图；置信度与 IoU 随统一 input 提交。
package humanyolo

import (
	"context"
	"io"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const (
	confidence = "0.35"
	iou        = "0.45"
	timeout    = 60 * time.Second
)

// Provider 只实现 TextProvider，返回上游 JSON，裁图留在衣橱。
type Provider struct {
	name    string
	gateway *vmindgateway.Client
}

func New(name string, gateway *vmindgateway.Client) *Provider {
	return &Provider{name: name, gateway: gateway}
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {

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
	response, err := p.gateway.Invoke(ctx, model, map[string]any{"file": imageBytes, "conf": confidence, "iou": iou}, timeout)
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
