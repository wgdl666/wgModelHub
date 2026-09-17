// Package photoroom 实现 Photoroom Remove Background（POST /v1/segment）。
// 只承接 OutputSpec.image：恰好一张输入图 → 一张内联透明 PNG；裁剪/填充/存储/业务重试留给调用方。
package photoroom

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	// DefaultBaseURL 是官方 Remove Background Basic plan 主机；路径固定为 /v1/segment。
	DefaultBaseURL = "https://sdk.photoroom.com"
	segmentPath    = "/v1/segment"
	pngMIME        = "image/png"
	pngSignature   = "\x89PNG\r\n\x1a\n"
)

// Provider 只实现 ImageProvider；凭据与 BaseURL 来自配置，调用方不得注入供应商地址或密钥。
type Provider struct {
	name    string
	apiKey  string
	baseURL string
	client  *http.Client
}

func New(name, apiKey, baseURL string) (*Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" API key is required")
	}
	if strings.TrimSpace(name) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "photoroom provider name is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	return &Provider{
		name:    name,
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  telemetry.NewHTTPClient(),
	}, nil
}

// GenerateImage 调用官方 multipart segment 接口；model 仅用于路由，不进入上游请求。
func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	_ = model // 路由层已选定本实例；Photoroom segment 无 request-level model 参数。
	if request == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "generate request is required")
	}
	media, err := exactlyOneImage(request.GetInput())
	if err != nil {
		return nil, err
	}
	data, mimeType, err := p.materializeInputImage(ctx, media)
	if err != nil {
		return nil, err
	}
	raw, err := p.doSegment(ctx, data, mimeType)
	if err != nil {
		return nil, err
	}
	if err := validatePNGResponse(raw); err != nil {
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

// exactlyOneImage 只接受恰好一张图片；多图/无图在发上游前拒绝，避免误用生图协议。
func exactlyOneImage(input *modelhubv2.Input) (*modelhubv2.Media, error) {
	images := provider.ImageMedias(input)
	switch len(images) {
	case 0:
		return nil, provider.New(provider.ErrorInvalidArgument, "photoroom remove background requires exactly one input image")
	case 1:
		return images[0], nil
	default:
		return nil, provider.New(provider.ErrorInvalidArgument, "photoroom remove background accepts exactly one input image")
	}
}

func (p *Provider) materializeInputImage(ctx context.Context, media *modelhubv2.Media) ([]byte, string, error) {
	if media == nil {
		return nil, "", provider.New(provider.ErrorInvalidArgument, "input image is required")
	}
	switch source := media.Source.(type) {
	case *modelhubv2.Media_Data:
		// 内联 MIME/非空/大小已由 service validateMedia 保证；此处只确认可上传内容。
		if len(source.Data) == 0 {
			return nil, "", provider.New(provider.ErrorInvalidArgument, "input image has no content")
		}
		mimeType := strings.TrimSpace(media.GetMimeType())
		if normalizedImageMIME(mimeType) == "" {
			return nil, "", provider.New(provider.ErrorInvalidArgument, "input image MIME is not image/*")
		}
		return source.Data, mimeType, nil
	case *modelhubv2.Media_Uri:
		return p.fetchImageURI(ctx, source.Uri)
	default:
		return nil, "", provider.New(provider.ErrorInvalidArgument, "input image source is required")
	}
}

func (p *Provider) fetchImageURI(ctx context.Context, rawURL string) ([]byte, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, "", provider.New(provider.ErrorInvalidArgument, "input image uri is empty")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", provider.Wrap(provider.ErrorInvalidArgument, "input image uri is invalid", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, "", provider.New(provider.ErrorInvalidArgument, "input image uri must be http or https")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", provider.Wrap(provider.ErrorInvalidArgument, "create input image request", err)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", provider.Wrap(provider.ErrorUnavailable, p.name+" fetch input image failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", provider.FromHTTP(p.name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(protocol.MaxMediaBytes)+1))
	if err != nil {
		return nil, "", provider.Wrap(provider.ErrorUnavailable, p.name+" read input image failed", err)
	}
	if len(data) == 0 {
		return nil, "", provider.New(provider.ErrorInvalidArgument, "input image has no content")
	}
	if len(data) > protocol.MaxMediaBytes {
		return nil, "", provider.Errorf(provider.ErrorInvalidArgument, "input image exceeds %d bytes", protocol.MaxMediaBytes)
	}
	mimeType, err := resolveInputImageMIME(resp.Header.Get("Content-Type"), data)
	if err != nil {
		return nil, "", err
	}
	return data, mimeType, nil
}

// resolveInputImageMIME 优先信任 Content-Type；缺失或非 image/* 时用内容嗅探，仍非 image/* 则拒绝。
func resolveInputImageMIME(contentType string, data []byte) (string, error) {
	if mime := normalizedImageMIME(contentType); mime != "" {
		return mime, nil
	}
	if mime := normalizedImageMIME(http.DetectContentType(data)); mime != "" {
		return mime, nil
	}
	return "", provider.New(provider.ErrorInvalidArgument, "input image MIME is not image/*")
}

func normalizedImageMIME(contentType string) string {
	baseType := strings.TrimSpace(contentType)
	if i := strings.Index(baseType, ";"); i >= 0 {
		baseType = strings.TrimSpace(baseType[:i])
	}
	if strings.HasPrefix(strings.ToLower(baseType), "image/") {
		return baseType
	}
	return ""
}

func (p *Provider) doSegment(ctx context.Context, data []byte, mimeType string) ([]byte, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	// 与现网 Muse /v1/segment 契约一致：只发送 image_file，不下发 format 等推测字段。
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image_file"; filename="%s"`, inputImageFilename(mimeType)))
	partHeader.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build segment request failed", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build segment request failed", err)
	}
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" build segment request failed", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.segmentURL(), &buf)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, p.name+" create request failed", err)
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("x-api-key", p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" request failed", err)
	}
	defer resp.Body.Close()
	// 错误正文只用于状态码分类，绝不回传给调用方，避免泄露供应商细节或密钥痕迹。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(protocol.MaxMediaBytes)+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read failed", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	if len(raw) > protocol.MaxMediaBytes {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "image exceeds %d bytes", protocol.MaxMediaBytes)
	}
	return raw, nil
}

func (p *Provider) segmentURL() string {
	return p.baseURL + segmentPath
}

func inputImageFilename(mimeType string) string {
	baseType := strings.TrimSpace(mimeType)
	if i := strings.Index(baseType, ";"); i >= 0 {
		baseType = strings.TrimSpace(baseType[:i])
	}
	ext := ".bin"
	switch strings.ToLower(baseType) {
	case "image/png":
		ext = ".png"
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	return "image" + ext
}

// validatePNGResponse 拒绝空响应与仅有签名/截断的伪 PNG；不在此强制 alpha，由实网 smoke 验收透明通道。
func validatePNGResponse(data []byte) error {
	if len(data) == 0 {
		return provider.New(provider.ErrorInvalidResponse, "photoroom returned empty image")
	}
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return provider.New(provider.ErrorInvalidResponse, "photoroom response is not a PNG image")
	}
	return nil
}

var _ provider.ImageProvider = (*Provider)(nil)
