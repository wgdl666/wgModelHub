package geminivideo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

// NewVertex 共用文本/生图的 ADC client，视频单独设置超时，避免改动同步请求的行为。
func NewVertex(name, project, outputURI string, client *http.Client) *Provider {
	videoClient := *client
	videoClient.Timeout = defaultHTTPTimeout
	return &Provider{name: name, baseURL: "https://aiplatform.googleapis.com/v1beta1/projects/" + url.PathEscape(project) + "/locations/global", client: &videoClient, pollInterval: 5 * time.Second, vertex: true, vertexOutputURI: outputURI}
}

// Vertex 使用 response_format 数组和 GCS 输出，不能照搬 Developer API 的 Files 上传协议。
// 当前业务只需首帧图生视频；编辑请求在出站前明确拒绝，避免误走另一渠道的上传接口。
func (p *Provider) buildVertexBody(ctx context.Context, model string, request *modelhubv2.GenerateRequest) ([]byte, error) {
	if model != models.GeminiOmniFlashPreview && model != models.GeminiOmni11FlashPreview {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Vertex video requires a Gemini Omni model")
	}
	if p.vertexOutputURI == "" {
		return nil, provider.NotAttempted(provider.ErrorConfiguration, "vertexai.video_output_uri is required for Omni video")
	}
	if provider.FirstVideoMedia(request.GetInput()) != nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Vertex Omni video editing is not supported")
	}
	prompt := provider.JoinedText(request.GetInput())
	if strings.TrimSpace(prompt) == "" {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "video prompt text is required in input")
	}
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 1 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Vertex Omni requires exactly one first-frame image")
	}
	out := request.GetOutput().GetVideo()
	duration := out.GetDurationSeconds()
	if duration != 0 && (duration < 3 || duration > 10) {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Vertex Omni duration must be 3 to 10 seconds")
	}
	ratio := out.GetAspectRatio()
	if ratio != "" && ratio != "16:9" && ratio != "9:16" {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Vertex Omni aspect ratio must be 16:9 or 9:16")
	}
	resolution := strings.ToLower(strings.TrimSpace(out.GetResolution()))
	if resolution != "" && resolution != "360p" && resolution != "720p" && resolution != "1080p" && resolution != "4k" {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "unsupported Vertex Omni resolution")
	}
	if model == models.GeminiOmniFlashPreview && resolution != "" && resolution != "720p" {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Omni Flash preview only supports 720p")
	}
	data, mime, err := p.loadMediaBytes(ctx, images[0], protocol.MaxMediaBytes)
	if err != nil {
		return nil, err
	}
	if mime == "" {
		mime = "image/png"
	}
	format := map[string]string{"type": "video", "delivery": "uri", "gcs_uri": p.vertexOutputURI}
	if duration != 0 {
		format["duration"] = fmt.Sprintf("%ds", duration)
	}
	if ratio != "" {
		format["aspect_ratio"] = ratio
	}
	if resolution != "" {
		format["resolution"] = resolution
	}
	return json.Marshal(map[string]any{
		"model": model, "background": true, "store": true, "stream": false,
		"input": []map[string]any{{"type": "user_input", "content": []map[string]string{
			{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mime_type": mime},
			{"type": "text", "text": augmentPromptForSilentVideo(prompt)},
		}}},
		"generation_config": map[string]any{"video_config": map[string]string{"task": "image_to_video"}},
		"response_format":   []map[string]string{format},
	})
}

// GCS 成片按对象下载 API 流式读取；不加载整段 base64，也不把 ADC 发到上游返回的任意地址。
func vertexDownloadURL(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "gs" || u.Host == "" || strings.TrimPrefix(u.Path, "/") == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", provider.New(provider.ErrorInvalidResponse, "Vertex Omni returned an invalid GCS video URI")
	}
	return "https://storage.googleapis.com/storage/v1/b/" + url.PathEscape(u.Host) + "/o/" + url.PathEscape(strings.TrimPrefix(u.Path, "/")) + "?alt=media", nil
}
