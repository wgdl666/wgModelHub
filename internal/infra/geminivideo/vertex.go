package geminivideo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

// NewVertex 共用文本/生图的 ADC client，视频单独设置超时，避免改动同步请求的行为。
func NewVertex(name, project string, client *http.Client) *Provider {
	videoClient := *client
	videoClient.Timeout = defaultHTTPTimeout
	return &Provider{name: name, baseURL: "https://aiplatform.googleapis.com/v1beta1/projects/" + url.PathEscape(project) + "/locations/global", client: &videoClient, pollInterval: 5 * time.Second, vertex: true}
}

// Vertex 使用 response_format 数组并直接返回视频字节，不能照搬 Developer API 的 Files 上传协议。
// 当前业务只需首帧图生视频；编辑请求在出站前明确拒绝，避免误走另一渠道的上传接口。
func (p *Provider) buildVertexBody(ctx context.Context, model string, request *modelhubv2.GenerateRequest) ([]byte, error) {
	if model != models.GeminiOmniFlashPreview && model != models.GeminiOmni11FlashPreview {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "Vertex video requires a Gemini Omni model")
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
	format := map[string]string{"type": "video"}
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

// 内联 JSON 已占用编码内存；流式解码到临时文件，避免再分配一份完整视频字节。
func (p *Provider) readInlineVideo(ctx context.Context, data, mimeType, responseID string, elapsed int64, emit provider.EmitEvent) error {
	if data == "" {
		return provider.New(provider.ErrorInvalidResponse, "Vertex video missing inline data")
	}
	tmp, err := os.CreateTemp("", "vertex-video-*.mp4")
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "create video temp file", err)
	}
	defer os.Remove(tmp.Name())
	decoded := base64.NewDecoder(base64.StdEncoding, strings.NewReader(data))
	n, copyErr := io.Copy(tmp, io.LimitReader(decoded, int64(protocol.MaxVideoBytes)+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return provider.New(provider.ErrorInvalidResponse, "Vertex video contains invalid base64")
	}
	if closeErr != nil {
		return provider.Wrap(provider.ErrorUnavailable, "close video temp file", closeErr)
	}
	if n == 0 || n > int64(protocol.MaxVideoBytes) {
		return provider.New(provider.ErrorInvalidResponse, "Vertex video is empty or exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return emitSilentVideoFile(ctx, tmp.Name(), mimeType, responseID, elapsed, emit)
}
