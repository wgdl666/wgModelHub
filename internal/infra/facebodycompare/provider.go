// Package facebodycompare 把阿里云 CompareFace 收成 ModelHub 文本结果。
// 调用方只传两张内联图；端点和密钥留在供应商配置，不从请求带入。
package facebodycompare

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	facebody "github.com/alibabacloud-go/facebody-20191230/v4/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	connectTimeoutMS = 5_000
	readTimeoutMS    = 15_000
)

type compareAPI interface {
	CompareFaceAdvance(*facebody.CompareFaceAdvanceRequest, *util.RuntimeOptions) (*facebody.CompareFaceResponse, error)
}

// Provider 只实现 TextProvider。第一张图是原图，第二张图是生成图。
type Provider struct {
	name   string
	client compareAPI
}

func New(name, endpoint, accessKey, secret string) (*Provider, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimSpace(endpoint)
	accessKey = strings.TrimSpace(accessKey)
	secret = strings.TrimSpace(secret)
	if name == "" || endpoint == "" || accessKey == "" || secret == "" {
		return nil, provider.New(provider.ErrorConfiguration, "facebody compare requires endpoint and a key pair")
	}
	client, err := facebody.NewClient(&openapi.Config{
		AccessKeyId:     tea.String(accessKey),
		AccessKeySecret: tea.String(secret),
		Endpoint:        tea.String(endpoint),
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "create facebody client", err)
	}
	return &Provider{name: name, client: client}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.FacebodyCompareFace {
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "facebody-compare does not serve model %q", model)
	}
	if err := ctx.Err(); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" compare canceled", err)
	}
	if p == nil || p.client == nil {
		return nil, provider.New(provider.ErrorConfiguration, "facebody client is not configured")
	}
	source, generated, err := twoImages(request)
	if err != nil {
		return nil, err
	}
	runtime := &util.RuntimeOptions{}
	runtime.SetConnectTimeout(connectTimeoutMS)
	runtime.SetReadTimeout(readTimeoutMS)
	runtime.SetAutoretry(false)
	runtime.SetMaxAttempts(1)
	response, err := p.client.CompareFaceAdvance(&facebody.CompareFaceAdvanceRequest{
		ImageURLAObject: bytes.NewReader(source),
		ImageURLBObject: bytes.NewReader(generated),
	}, runtime)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" compare face", err)
	}
	if response == nil || response.Body == nil || response.Body.Data == nil || response.Body.Data.Confidence == nil {
		return nil, provider.New(provider.ErrorInvalidResponse, "facebody compare returned no confidence")
	}
	body, err := json.Marshal(map[string]float64{"similarity": normalizeConfidence(float64(tea.Float32Value(response.Body.Data.Confidence)))})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode similarity", err)
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

func twoImages(request *modelhubv2.GenerateRequest) ([]byte, []byte, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	images := provider.ImageMedias(input)
	if len(images) != 2 {
		return nil, nil, provider.NotAttempted(provider.ErrorInvalidArgument, "face compare requires exactly two input images")
	}
	source, ok := imageBytes(images[0])
	if !ok {
		return nil, nil, provider.NotAttempted(provider.ErrorInvalidArgument, "face compare requires inline image bytes")
	}
	generated, ok := imageBytes(images[1])
	if !ok {
		return nil, nil, provider.NotAttempted(provider.ErrorInvalidArgument, "face compare requires inline image bytes")
	}
	return source, generated, nil
}

func imageBytes(media *modelhubv2.Media) ([]byte, bool) {
	data, ok := media.Source.(*modelhubv2.Media_Data)
	return data.Data, ok && len(data.Data) > 0
}

// normalizeConfidence 把 Facebody 的 0–100 收成调用方使用的 0–1。已经是 0–1 的值保持不动。
func normalizeConfidence(confidence float64) float64 {
	if confidence > 1 {
		confidence /= 100
	}
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}
