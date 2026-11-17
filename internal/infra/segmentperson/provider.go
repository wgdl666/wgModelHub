// Package segmentperson 承接国内自建 Bria 抠图。
// 人物与商品主体使用真实模型 ID；Gateway 按登记协议选择执行端。
// 裁剪、OSS 落盘和业务重试留在调用方。
package segmentperson

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const (
	pngMIME = "image/png"
	// 国内服务偶发慢响应；超时与原先衣橱直连一致，避免 ModelHub 无限挂起。
	requestTimeout = 180 * time.Second
)

// Provider 只转换抠图输入和结果；不选择原生端点。
type Provider struct {
	name                                string
	gateway                             *vmindgateway.Client
	method, personMethod, subjectMethod string
}

func New(name string, gateway *vmindgateway.Client, method, personMethod, subjectMethod string) *Provider {
	return &Provider{name: name, gateway: gateway, method: strings.TrimSpace(method), personMethod: strings.TrimSpace(personMethod), subjectMethod: strings.TrimSpace(subjectMethod)}
}

// GenerateImage 提交一次 Gateway 请求，返回一张透明 PNG。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if request == nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "generate request is required")
	}
	media, err := exactlyOneImage(request.GetInput())
	if err != nil {
		return nil, err
	}
	data, err := inlineImage(media)
	if err != nil {
		return nil, err
	}
	raw, err := p.doSegment(ctx, model, data)
	if err != nil {
		return nil, err
	}
	// 抠图结果按上游 PNG 契约原样转交；像素解码留给调用方实际处理图片时完成，避免网关重复解码。
	// doSegment 仍负责 HTTP、JSON、Base64 和非空响应检查。
	return &modelhubv2.GenerateEvent{
		Final: true,
		Items: []*modelhubv2.OutputItem{{
			Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
				MimeType: pngMIME,
				Source:   &modelhubv2.Media_Data{Data: raw},
			}},
		}},
	}, nil
}

func exactlyOneImage(input *modelhubv2.Input) (*modelhubv2.Media, error) {
	images := provider.ImageMedias(input)
	switch len(images) {
	case 0:
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "segment-person requires exactly one input image")
	case 1:
		return images[0], nil
	default:
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "segment-person accepts exactly one input image")
	}
}

func inlineImage(media *modelhubv2.Media) ([]byte, error) {
	if media == nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "input image is required")
	}
	data, ok := media.Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "segment-person requires inline image bytes")
	}
	return data.Data, nil
}

func (p *Provider) methodFor(person bool) string {
	if person && p.personMethod != "" {
		return p.personMethod
	}
	if !person && p.subjectMethod != "" {
		return p.subjectMethod
	}
	return p.method
}

func (p *Provider) doSegment(ctx context.Context, model string, imageBytes []byte) ([]byte, error) {
	fields := map[string]string{
		"image":  base64.StdEncoding.EncodeToString(imageBytes),
		"method": p.methodFor(false),
		"output": "b64",
	}
	// 算法参数按登记协议在 Gateway 中选择；ModelHub 不再根据模型名选择端点。
	fields["person_method"], fields["subject_method"] = p.methodFor(true), p.methodFor(false)
	// 人物和商品抠图都走薇光点亮。上游不读 traceparent，表单 trace_id 才能和账本对上。
	// 没有有效 span 时不写空字段，避免上游把空字符串当成一次真实追踪。
	if traceID := telemetry.TraceID(ctx); traceID != "" {
		fields["trace_id"] = traceID
	}
	response, err := p.gateway.Invoke(ctx, model, fields, requestTimeout)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" segment request failed", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		preview, _ := io.ReadAll(io.LimitReader(response.Body, 500))
		return nil, provider.FromHTTPDetail(p.name, response.StatusCode, strings.TrimSpace(string(preview)))
	}
	var payload struct {
		RefinedPNGBase64 string `json:"refined_png_b64"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<20)).Decode(&payload); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode segment response", err)
	}
	cutout, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.RefinedPNGBase64))
	if err != nil || len(cutout) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, p.name+" response is missing refined_png_b64")
	}
	return cutout, nil
}
