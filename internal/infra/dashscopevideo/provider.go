package dashscopevideo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	defaultBaseURL      = "https://dashscope.aliyuncs.com/api/v1"
	createPath          = "/services/aigc/video-generation/video-synthesis"
	defaultPollInterval = 3 * time.Second
	defaultMaxPollTime  = 10 * time.Minute
	maxReferenceImgs    = 4
	videoMIMEType       = "video/mp4"
)

type Provider struct {
	name         string
	apiKey       string
	baseURL      string
	pollInterval time.Duration
	maxPollTime  time.Duration
	client       *http.Client
}

func New(name, apiKey, baseURL string, pollInterval, maxPollTime float64) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" api_key is required")
	}
	baseURL = normalizeBaseURL(baseURL)
	// YAML 省略 poll 字段时沿用 Dev 直连默认，与 Config.Validate 接受 0 的语义一致。
	if pollInterval <= 0 {
		pollInterval = float64(defaultPollInterval) / float64(time.Second)
	}
	if maxPollTime <= 0 {
		maxPollTime = float64(defaultMaxPollTime) / float64(time.Second)
	}
	return &Provider{
		name:         name,
		apiKey:       apiKey,
		baseURL:      baseURL,
		pollInterval: time.Duration(pollInterval * float64(time.Second)),
		maxPollTime:  time.Duration(maxPollTime * float64(time.Second)),
		client:       telemetry.NewHTTPClient(),
	}, nil
}

// GenerateVideo 复用 Submit/Get/ReadResult；前台等待受 maxPollTime 限制，异步 Submit/Get 不受影响。
func (p *Provider) GenerateVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) error {
	ctx, cancel := context.WithTimeout(ctx, p.maxPollTime)
	defer cancel()
	err := provider.RunVideoJob(ctx, p, model, request, emit)
	if errors.Is(err, context.DeadlineExceeded) {
		return provider.Errorf(provider.ErrorTimeout, "%s video generation timed out", p.name)
	}
	return err
}

// SubmitVideo 按模型分支创建 DashScope 异步任务；wan2.7-videoedit 走编辑请求形态。
func (p *Provider) SubmitVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (string, error) {
	if request == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "video request is required")
	}
	videoOut := request.GetOutput().GetVideo()
	resolution := ""
	var duration int
	if videoOut != nil {
		resolution = videoOut.GetResolution()
		if videoOut.DurationSeconds != nil {
			duration = int(videoOut.GetDurationSeconds())
		}
	}
	prompt := provider.JoinedText(request.GetInput())
	if strings.TrimSpace(prompt) == "" {
		return "", provider.New(provider.ErrorInvalidArgument, "video prompt text is required in input")
	}

	hasVideo := provider.FirstVideoMedia(request.GetInput()) != nil
	if model == models.Wan27VideoEdit {
		if !hasVideo {
			return "", provider.New(provider.ErrorInvalidArgument, "wan2.7-videoedit requires video in input")
		}
		return p.submitEdit(ctx, model, request, prompt, resolution)
	}
	if hasVideo {
		return "", provider.New(provider.ErrorInvalidArgument, "DashScope generation model does not accept video input")
	}
	return p.submitI2V(ctx, model, request, prompt, resolution, duration)
}

// GetVideo 单次查询 /tasks/{id}；SUCCEEDED 即使无 URL 也视为成功，空 URL 由 ReadVideoResult 报错。
func (p *Provider) GetVideo(ctx context.Context, _ string, providerTaskID string) (provider.VideoJob, error) {
	status, _, code, message, err := p.getTask(ctx, providerTaskID)
	if err != nil {
		return provider.VideoJob{}, err
	}
	pollMs := int32(p.pollInterval / time.Millisecond)
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCEEDED":
		return provider.VideoJob{State: provider.VideoJobSucceeded, PollAfterMs: pollMs}, nil
	case "FAILED", "CANCELED", "UNKNOWN", "FAILURE", "ERROR":
		msg := strings.TrimSpace(message)
		if msg == "" {
			msg = status
		}
		var jobErr error
		if code != "" {
			jobErr = provider.Errorf(provider.ErrorUnavailable, "%s task %s: %s: %s", p.name, status, code, msg)
		} else {
			jobErr = provider.Errorf(provider.ErrorUnavailable, "%s task %s: %s", p.name, status, msg)
		}
		return provider.VideoJob{State: provider.VideoJobFailed, PollAfterMs: pollMs, Err: jobErr}, nil
	default:
		return provider.VideoJob{State: provider.VideoJobRunning, PollAfterMs: pollMs}, nil
	}
}

// ReadVideoResult 再次 getTask 取 video_url 并流式下载分块 emit，不 ReadAll 整段视频。
func (p *Provider) ReadVideoResult(ctx context.Context, _ string, providerTaskID string, emit provider.EmitEvent) error {
	_, videoURL, _, _, err := p.getTask(ctx, providerTaskID)
	if err != nil {
		return err
	}
	if videoURL == "" {
		return provider.New(provider.ErrorInvalidResponse, p.name+" task succeeded but video_url empty")
	}
	response, err := provider.OpenPublicURL(ctx, p.client, p.name, videoURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return provider.EmitVideoChunksFromReader(response.Body, videoMIMEType, providerTaskID, 0, emit)
}

func (p *Provider) submitI2V(
	ctx context.Context,
	model string,
	request *modelhubv2.GenerateRequest,
	prompt, resolution string,
	duration int,
) (string, error) {
	imageURL, err := p.resolveImageURL(provider.FirstImageMedia(request.GetInput()))
	if err != nil {
		return "", err
	}
	return p.createI2VTask(ctx, model, imageURL, prompt, resolution, duration)
}

func (p *Provider) submitEdit(
	ctx context.Context,
	model string,
	request *modelhubv2.GenerateRequest,
	prompt, resolution string,
) (string, error) {
	videoURL, err := p.resolveVideoURL(provider.FirstVideoMedia(request.GetInput()))
	if err != nil {
		return "", err
	}
	refURLs := make([]string, 0, len(provider.ImageMedias(request.GetInput())))
	for _, image := range provider.ImageMedias(request.GetInput()) {
		refURL, refErr := p.resolveImageURL(image)
		if refErr != nil {
			return "", refErr
		}
		refURLs = append(refURLs, refURL)
		if len(refURLs) > maxReferenceImgs {
			return "", provider.New(provider.ErrorInvalidArgument, "at most 4 reference images for video edit")
		}
	}
	if model == "" {
		model = models.Wan27VideoEdit
	}
	return p.createEditTask(ctx, model, videoURL, prompt, refURLs, resolution)
}

func (p *Provider) resolveImageURL(media *modelhubv2.Media) (string, error) {
	if media == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "image is required in input")
	}
	if uri := provider.MediaURI(media); uri != "" {
		return uri, nil
	}
	return "", provider.New(provider.ErrorInvalidArgument, "DashScope video requires image uri; inline bytes are not uploaded here")
}

func (p *Provider) resolveVideoURL(media *modelhubv2.Media) (string, error) {
	if media == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "video is required in input")
	}
	if uri := provider.MediaURI(media); uri != "" {
		return uri, nil
	}
	return "", provider.New(provider.ErrorInvalidArgument, "DashScope video edit requires video uri")
}

func (p *Provider) createI2VTask(ctx context.Context, model, imageURL, prompt, resolution string, duration int) (string, error) {
	// Wan2.7/Kling 等新模型用 media first_frame；旧 Wan/HappyHorse 仍走 img_url + 大写 resolution 分支。
	var input map[string]any
	var parameters map[string]any
	if usesMediaFirstFrameI2VFormat(model) {
		input = map[string]any{
			"prompt": prompt,
			"media":  []map[string]any{{"type": "first_frame", "url": imageURL}},
		}
		if isKlingVideoModel(model) {
			if duration <= 0 {
				duration = 5
			}
			parameters = map[string]any{
				"mode":      klingModeFromResolution(resolution),
				"duration":  duration,
				"audio":     false,
				"watermark": false,
			}
		} else {
			parameters = map[string]any{
				"resolution": normalizeWan27Resolution(resolution),
				"watermark":  false,
			}
			if isWan27I2VModel(model) {
				parameters["prompt_extend"] = false
			}
			if duration > 0 {
				parameters["duration"] = duration
			}
		}
	} else {
		input = map[string]any{"prompt": prompt, "img_url": imageURL}
		parameters = map[string]any{
			"prompt_extend": false,
			"resolution":    strings.ToUpper(strings.TrimSpace(resolution)),
			"shot_type":     "single",
			"watermark":     false,
		}
		if parameters["resolution"] == "" {
			parameters["resolution"] = "480P"
		}
		if duration > 0 {
			parameters["duration"] = duration
		}
		if isWan26I2VFlashModel(model) {
			parameters["audio"] = false
		}
	}
	return p.createTask(ctx, model, input, parameters)
}

func (p *Provider) createEditTask(ctx context.Context, model, videoURL, prompt string, refURLs []string, resolution string) (string, error) {
	media := []map[string]any{{"type": "video", "url": videoURL}}
	for _, refURL := range refURLs {
		media = append(media, map[string]any{"type": "reference_image", "url": refURL})
	}
	parameters := map[string]any{
		"resolution":    normalizeWan27Resolution(resolution),
		"prompt_extend": true,
		"watermark":     false,
	}
	input := map[string]any{"prompt": prompt, "media": media}
	return p.createTask(ctx, model, input, parameters)
}

func (p *Provider) createTask(ctx context.Context, model string, input, parameters map[string]any) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"input":      input,
		"parameters": parameters,
	})
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+createPath, bytes.NewReader(body))
	if err != nil {
		return "", provider.Wrap(provider.ErrorInvalidArgument, "create submit request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("X-DashScope-Async", "enable")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", provider.Wrap(provider.ErrorUnavailable, p.name+" create failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", provider.Wrap(provider.ErrorUnavailable, p.name+" read create response", err)
	}
	if resp.StatusCode >= 400 {
		return "", provider.FromHTTP(p.name, resp.StatusCode)
	}
	var envelope struct {
		Output struct {
			TaskID string `json:"task_id"`
		} `json:"output"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode create response", err)
	}
	if envelope.Code != "" {
		return "", provider.Errorf(provider.ErrorUnavailable, "%s %s: %s", p.name, envelope.Code, envelope.Message)
	}
	taskID := strings.TrimSpace(envelope.Output.TaskID)
	if taskID == "" {
		return "", provider.New(provider.ErrorInvalidResponse, p.name+" create returned empty task_id")
	}
	return taskID, nil
}

func (p *Provider) getTask(ctx context.Context, taskID string) (status, videoURL, code, message string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/tasks/"+taskID, nil)
	if err != nil {
		return "", "", "", "", provider.Wrap(provider.ErrorInvalidArgument, "create poll request", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", "", "", ctx.Err()
		}
		return "", "", "", "", provider.Wrap(provider.ErrorUnavailable, p.name+" poll failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", "", "", "", provider.Wrap(provider.ErrorUnavailable, p.name+" read poll response", err)
	}
	if resp.StatusCode >= 400 {
		return "", "", "", "", provider.FromHTTP(p.name, resp.StatusCode)
	}
	var envelope struct {
		Output struct {
			TaskStatus string          `json:"task_status"`
			VideoURL   string          `json:"video_url"`
			Code       string          `json:"code"`
			Message    string          `json:"message"`
			Results    json.RawMessage `json:"results"`
		} `json:"output"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", "", "", "", provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode poll response", err)
	}
	if envelope.Code != "" {
		return "", "", envelope.Code, envelope.Message, provider.Errorf(provider.ErrorUnavailable, "%s %s: %s", p.name, envelope.Code, envelope.Message)
	}
	videoURL = strings.TrimSpace(envelope.Output.VideoURL)
	if videoURL == "" {
		videoURL = videoURLFromResults(envelope.Output.Results)
	}
	return envelope.Output.TaskStatus, videoURL, envelope.Output.Code, envelope.Output.Message, nil
}

func normalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return defaultBaseURL
	}
	if strings.HasSuffix(base, "/compatible-mode/v1") {
		return strings.TrimSuffix(base, "/compatible-mode/v1") + "/api/v1"
	}
	return base
}

func isWan26I2VFlashModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "wan2.6") && strings.Contains(model, "i2v") && strings.Contains(model, "flash")
}

func isWan27I2VModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "wan2.7") && strings.Contains(model, "i2v")
}

func isHappyHorseI2VModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "happyhorse") && strings.Contains(model, "i2v")
}

func isKlingVideoModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "kling") && strings.Contains(model, "video")
}

func usesMediaFirstFrameI2VFormat(model string) bool {
	return isWan27I2VModel(model) || isHappyHorseI2VModel(model) || isKlingVideoModel(model)
}

func klingModeFromResolution(resolution string) string {
	if strings.EqualFold(strings.TrimSpace(resolution), "1080p") {
		return "pro"
	}
	return "std"
}

func normalizeWan27Resolution(resolution string) string {
	res := strings.ToUpper(strings.TrimSpace(resolution))
	switch res {
	case "720P", "1080P":
		return res
	default:
		return "720P"
	}
}

func videoURLFromResults(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj struct {
		VideoURL string `json:"video_url"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && strings.TrimSpace(obj.VideoURL) != "" {
		return strings.TrimSpace(obj.VideoURL)
	}
	var arr []struct {
		VideoURL string `json:"video_url"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, item := range arr {
			if u := strings.TrimSpace(firstNonEmpty(item.VideoURL, item.URL)); u != "" {
				return u
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var _ provider.VideoProvider = (*Provider)(nil)
