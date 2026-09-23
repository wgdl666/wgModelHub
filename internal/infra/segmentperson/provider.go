// Package segmentperson 承接国内自建 Bria 抠图。
// 人物与商品主体是两个端点、两个真实模型 ID；凭据和 method 只来自供应商配置。
// 裁剪、OSS 落盘和业务重试留在调用方。
package segmentperson

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/png"
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
	personPath  = "/api/segment"
	subjectPath = "/api/segment_subject"
	pngMIME     = "image/png"
	// 国内服务偶发慢响应；超时与原先衣橱直连一致，避免 ModelHub 无限挂起。
	requestTimeout = 180 * time.Second
)

// Provider 只实现 ImageProvider。model 选择端点，不进入上游请求体。
type Provider struct {
	name     string
	baseURL  string
	username string
	password string
	method   string
	client   *http.Client
}

func New(name, baseURL, username, password, method string) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	method = strings.TrimSpace(method)
	if name == "" {
		return nil, provider.New(provider.ErrorConfiguration, "segment-person provider name is required")
	}
	if baseURL == "" || method == "" {
		return nil, provider.New(provider.ErrorConfiguration, "segment-person base_url and method are required")
	}
	if (username == "") != (password == "") {
		return nil, provider.New(provider.ErrorConfiguration, "segment-person username and password must be configured together")
	}
	client := telemetry.NewHTTPClient()
	client.Timeout = requestTimeout
	return &Provider{
		name:     name,
		baseURL:  baseURL,
		username: username,
		password: password,
		method:   method,
		client:   client,
	}, nil
}

// GenerateImage 按模型 ID 选择人物或商品主体端点，返回一张透明 PNG。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if request == nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "generate request is required")
	}
	endpoint, person, err := endpointFor(model)
	if err != nil {
		return nil, err
	}
	media, err := exactlyOneImage(request.GetInput())
	if err != nil {
		return nil, err
	}
	data, err := inlineImage(media)
	if err != nil {
		return nil, err
	}
	raw, err := p.doSegment(ctx, endpoint, data, person)
	if err != nil {
		return nil, err
	}
	if err := validatePNG(raw); err != nil {
		return nil, err
	}
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

func endpointFor(model string) (string, bool, error) {
	switch model {
	case models.SegmentPersonBria:
		return personPath, true, nil
	case models.SegmentSubjectBria:
		return subjectPath, false, nil
	default:
		return "", false, provider.NotAttemptedf(provider.ErrorInvalidArgument, "segment-person does not serve model %q", model)
	}
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

func (p *Provider) doSegment(ctx context.Context, endpoint string, imageBytes []byte, person bool) ([]byte, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"image":  base64.StdEncoding.EncodeToString(imageBytes),
		"method": p.method,
		"output": "b64",
	}
	// 人物接口沿用现网约定：preprocess 字段存在且值为字符串 "false"。商品接口不带该字段。
	if person {
		fields["preprocess"] = "false"
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return nil, provider.WrapNotAttempted(provider.ErrorUnavailable, p.name+" build segment request", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" finish segment request", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+endpoint, &body)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" create segment request", err)
	}
	httpReq.Header.Set("content-type", writer.FormDataContentType())
	if p.username != "" {
		httpReq.SetBasicAuth(p.username, p.password)
	}
	response, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
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

func validatePNG(raw []byte) error {
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || format != "png" || decoded.Bounds().Dx() <= 0 || decoded.Bounds().Dy() <= 0 {
		return provider.New(provider.ErrorInvalidResponse, "segment-person returned an invalid PNG")
	}
	return nil
}
