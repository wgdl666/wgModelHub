// Package lenscorrection 只适配镜头校正 HTTP 契约；对象存储、处理标记和业务重试归调用方。
package lenscorrection

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

type Provider struct {
	name     string
	endpoint string
	client   *http.Client
}

func New(name, baseURL string) (*Provider, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if strings.TrimSpace(name) == "" || err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, provider.New(provider.ErrorConfiguration, "lens correction requires name and HTTP(S) base_url without credentials or query")
	}
	client := telemetry.NewHTTPClient()
	client.Timeout = 60 * time.Second
	return &Provider{name: name, endpoint: baseURL + "/correct", client: client}, nil
}

// 开关缺省 true，但显式 false 必须原样下发，不能用 bool 零值吞掉关闭请求。
type options struct {
	MirrorLens bool `json:"mirror_lens"`
	Crop       bool `json:"crop"`
}

type diagnostics struct {
	ContentType     string  `json:"content_type"`
	Bytes           int     `json:"bytes"`
	InWidth         int     `json:"in_width"`
	InHeight        int     `json:"in_height"`
	ProcessedWidth  int     `json:"processed_width"`
	ProcessedHeight int     `json:"processed_height"`
	Resized         bool    `json:"resized"`
	OutWidth        int     `json:"out_width"`
	OutHeight       int     `json:"out_height"`
	MirrorLens      bool    `json:"mirror_lens"`
	Crop            bool    `json:"crop"`
	ResampleMS      float64 `json:"resample_ms"`
	SharpenMS       float64 `json:"sharpen_ms"`
	// 这是上游重采样加锐化耗时，不能写成 ModelHub 完整调用耗时。
	ElapsedMS float64 `json:"elapsed_ms"`
}

func (p *Provider) GenerateImage(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if request == nil || model != models.LensDistortionCorrection {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "lens correction requires its model and image request")
	}
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 1 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "lens correction requires exactly one inline image")
	}
	data, ok := provider.InlineImageBytes(images[0])
	if !ok || len(data) > protocol.MaxMediaBytes {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "lens correction requires nonempty inline image within media limit")
	}
	opts := options{MirrorLens: true, Crop: true}
	if traits := strings.TrimSpace(request.GetInput().GetModelTraits()); traits != "" {
		if err := json.Unmarshal([]byte(traits), &opts); err != nil {
			return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "invalid lens correction model_traits", err)
		}
	}
	body, err := json.Marshal(struct {
		Base64 string `json:"base64"`
		options
	}{Base64: base64.StdEncoding.EncodeToString(data), options: opts})
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "encode lens correction request", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorConfiguration, "create lens correction request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" lens correction request failed", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		// 错误正文可能回显 Base64，禁止进入日志或 RPC 错误文本。
		return nil, provider.FromHTTPDetail(p.name, response.StatusCode, "lens correction upstream rejected request")
	}
	var payload struct {
		ImageBase64 string `json:"image_base64"`
		diagnostics
	}
	// JSON 包含 Base64，比图片大约多三分之一；边界必须覆盖允许的最大媒体。
	if err := json.NewDecoder(io.LimitReader(response.Body, int64(base64.StdEncoding.EncodedLen(protocol.MaxMediaBytes)+(1<<20)))).Decode(&payload); err != nil {
		return nil, provider.New(provider.ErrorInvalidResponse, "invalid lens correction response JSON")
	}
	pngBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.ImageBase64))
	if err != nil || len(pngBytes) == 0 || len(pngBytes) > protocol.MaxMediaBytes || payload.ContentType != "image/png" {
		return nil, provider.New(provider.ErrorInvalidResponse, "lens correction response is missing a valid PNG payload")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil || format != "png" || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width != payload.OutWidth || cfg.Height != payload.OutHeight || payload.Bytes != len(pngBytes) {
		return nil, provider.New(provider.ErrorInvalidResponse, "lens correction PNG dimensions or byte count mismatch")
	}
	metadata, err := json.Marshal(payload.diagnostics)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "encode lens correction diagnostics", err)
	}
	// 图片与诊断按顺序回传；不把服务端缩放再做一次，也不记录媒体正文。
	return &modelhubv2.GenerateEvent{Final: true, Items: []*modelhubv2.OutputItem{
		{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: pngBytes}}}},
		{Item: &modelhubv2.OutputItem_Text{Text: string(metadata)}},
	}}, nil
}
