// Package segmentperson 承接国内自建 Bria 抠图。
// 人物与商品主体是两个端点、两个真实模型 ID；凭据和 method 只来自供应商配置。
// 裁剪、OSS 落盘和业务重试留在调用方。
package segmentperson

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
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
	name          string
	baseURL       string
	username      string
	password      string
	method        string
	personMethod  string
	subjectMethod string
	client        *http.Client
}

func New(name, baseURL, username, password, method string) (*Provider, error) {
	return NewWithMethods(name, baseURL, username, password, method, "", "")
}

// NewWithMethods preserves legacy method while allowing endpoint-specific algorithms.
func NewWithMethods(name, baseURL, username, password, method, personMethod, subjectMethod string) (*Provider, error) {
	name = strings.TrimSpace(name)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	method = strings.TrimSpace(method)
	if name == "" {
		return nil, provider.New(provider.ErrorConfiguration, "segment-person provider name is required")
	}
	personMethod = strings.TrimSpace(personMethod)
	subjectMethod = strings.TrimSpace(subjectMethod)
	if baseURL == "" || (method == "" && (personMethod == "" || subjectMethod == "")) {
		return nil, provider.New(provider.ErrorConfiguration, "segment-person base_url and method are required")
	}
	if (username == "") != (password == "") {
		return nil, provider.New(provider.ErrorConfiguration, "segment-person username and password must be configured together")
	}
	client := telemetry.NewHTTPClient()
	client.Timeout = requestTimeout
	return &Provider{
		name:          name,
		baseURL:       baseURL,
		username:      username,
		password:      password,
		method:        method,
		personMethod:  personMethod,
		subjectMethod: subjectMethod,
		client:        client,
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

func (p *Provider) methodFor(person bool) string {
	if person && p.personMethod != "" {
		return p.personMethod
	}
	if !person && p.subjectMethod != "" {
		return p.subjectMethod
	}
	return p.method
}

func (p *Provider) doSegment(ctx context.Context, endpoint string, imageBytes []byte, person bool) ([]byte, error) {
	parts := make([]httpbody.Part, 0, 5)
	fields := map[string]string{
		"image":  base64.StdEncoding.EncodeToString(imageBytes),
		"method": p.methodFor(person),
		"output": "b64",
	}
	// 人物接口沿用现网约定：preprocess 字段存在且值为字符串 "false"。商品接口不带该字段。
	if person {
		fields["preprocess"] = "false"
	}
	// 人物和商品抠图都走薇光点亮。上游不读 traceparent，表单 trace_id 才能和账本对上。
	// 没有有效 span 时不写空字段，避免上游把空字符串当成一次真实追踪。
	if traceID := telemetry.TraceID(ctx); traceID != "" {
		fields["trace_id"] = traceID
	}
	for name, value := range fields {
		parts = append(parts, httpbody.Field(name, value))
	}
	body, err := httpbody.Multipart(parts)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorUnavailable, p.name+" build segment request", err)
	}
	// 抠图没有持久化副作用，瞬时连接故障允许最多三次请求；业务流程重跑仍由调用方负责。
	// 每次重建请求体，避免 broken pipe 后复用已消耗的 multipart 流。
	var response *http.Response
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		httpReq, err := body.NewRequest(ctx, http.MethodPost, p.baseURL+endpoint)
		if err != nil {
			return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, p.name+" create segment request", err)
		}
		if p.username != "" {
			httpReq.SetBasicAuth(p.username, p.password)
		}
		response, err = p.client.Do(httpReq)
		if ctx.Err() != nil {
			if response != nil {
				response.Body.Close()
			}
			return nil, ctx.Err()
		}
		retryable := transientConnectionError(err)
		statusCode := 0
		if err == nil {
			statusCode = response.StatusCode
			retryable = statusCode == 429 || statusCode == 500 || statusCode == 502 || statusCode == 503 || statusCode == 504
		}
		if !retryable || attempt == 3 {
			if err != nil {
				return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" segment request failed", err)
			}
			break
		}
		if response != nil {
			response.Body.Close()
		}
		logs.Default().WarnContext(ctx, "modelhub_segment_retry", "provider", p.name, "endpoint", endpoint, "attempt", attempt, "status_code", statusCode)
		// 退避共用调用方期限；取消、超时不能继续消耗算法资源。
		timer := time.NewTimer(time.Duration(attempt) * 300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
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

// 只重试明确的连接中断，避免把参数、证书、DNS 配置和超时错误当作瞬时故障。
func transientConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return false
	}
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
