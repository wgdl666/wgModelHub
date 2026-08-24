package arkvideo

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
)

const (
	defaultBaseURL      = "https://ark.cn-beijing.volces.com/api/v3"
	createPath          = "/contents/generations/tasks"
	defaultPollInterval = 3 * time.Second
	defaultMaxPollTime  = 10 * time.Minute
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
	// YAML 省略 poll 字段时沿用方舟直连默认，与 Config.Validate 接受 0 的语义一致。
	if pollInterval <= 0 {
		pollInterval = float64(defaultPollInterval) / float64(time.Second)
	}
	if maxPollTime <= 0 {
		maxPollTime = float64(defaultMaxPollTime) / float64(time.Second)
	}
	return &Provider{
		name:         name,
		apiKey:       apiKey,
		baseURL:      normalizeBaseURL(baseURL),
		pollInterval: time.Duration(pollInterval * float64(time.Second)),
		maxPollTime:  time.Duration(maxPollTime * float64(time.Second)),
		client:       telemetry.NewHTTPClient(),
	}, nil
}

func normalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return defaultBaseURL
	}
	return base
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

// SubmitVideo 只做 Seedance 2.5 第一刀：文本 + 首帧图 URI。视频参考/编辑/延长另开阶段。
func (p *Provider) SubmitVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (string, error) {
	if request == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "video request is required")
	}
	if provider.FirstVideoMedia(request.GetInput()) != nil {
		return "", provider.New(provider.ErrorInvalidArgument, "ark seedance 2.5 first cut does not accept video input")
	}
	prompt := provider.JoinedText(request.GetInput())
	if strings.TrimSpace(prompt) == "" {
		return "", provider.New(provider.ErrorInvalidArgument, "video prompt text is required in input")
	}
	imageURL, err := p.resolveImageURL(provider.FirstImageMedia(request.GetInput()))
	if err != nil {
		return "", err
	}
	videoOut := request.GetOutput().GetVideo()
	resolution := ""
	duration := 0
	aspectRatio := ""
	if videoOut != nil {
		resolution = videoOut.GetResolution()
		if videoOut.DurationSeconds != nil {
			duration = int(videoOut.GetDurationSeconds())
		}
		aspectRatio = videoOut.GetAspectRatio()
	}
	return p.createTask(ctx, model, imageURL, prompt, resolution, duration, aspectRatio)
}

func (p *Provider) GetVideo(ctx context.Context, _, providerTaskID string) (provider.VideoJob, error) {
	res, err := p.getTask(ctx, providerTaskID)
	if err != nil {
		return provider.VideoJob{}, err
	}
	pollMs := int32(p.pollInterval / time.Millisecond)
	switch strings.ToLower(strings.TrimSpace(res.status)) {
	case "succeeded", "success", "completed", "done":
		return provider.VideoJob{State: provider.VideoJobSucceeded, PollAfterMs: pollMs}, nil
	case "failed", "failure", "fail", "error", "cancelled", "canceled", "expired":
		msg := strings.TrimSpace(res.errorMessage)
		if msg == "" {
			msg = res.status
		}
		var jobErr error
		if code := strings.TrimSpace(res.errorCode); code != "" {
			jobErr = provider.Errorf(provider.ErrorUnavailable, "%s task %s: %s: %s", p.name, res.status, code, msg)
		} else {
			jobErr = provider.Errorf(provider.ErrorUnavailable, "%s task %s: %s", p.name, res.status, msg)
		}
		return provider.VideoJob{State: provider.VideoJobFailed, PollAfterMs: pollMs, Err: jobErr}, nil
	default:
		return provider.VideoJob{State: provider.VideoJobRunning, PollAfterMs: pollMs}, nil
	}
}

func (p *Provider) ReadVideoResult(ctx context.Context, _, providerTaskID string, emit provider.EmitEvent) error {
	res, err := p.getTask(ctx, providerTaskID)
	if err != nil {
		return err
	}
	if res.videoURL == "" {
		return provider.New(provider.ErrorInvalidResponse, p.name+" task succeeded but video_url empty")
	}
	response, err := provider.OpenPublicURL(ctx, p.client, p.name, res.videoURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return provider.EmitVideoChunksFromReader(response.Body, videoMIMEType, providerTaskID, 0, emit)
}

func (p *Provider) resolveImageURL(media *modelhubv2.Media) (string, error) {
	if media == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "first_frame image is required in input")
	}
	if uri := provider.MediaURI(media); uri != "" {
		return uri, nil
	}
	if data, ok := media.Source.(*modelhubv2.Media_Data); ok && len(data.Data) > 0 {
		return "", provider.New(provider.ErrorInvalidArgument, "ark video requires image uri for async create")
	}
	return "", provider.New(provider.ErrorInvalidArgument, "image source is required")
}

func (p *Provider) createTask(ctx context.Context, model, imageURL, prompt, resolution string, duration int, aspectRatio string) (string, error) {
	resolution, duration, aspectRatio = NormalizeParams(resolution, duration, aspectRatio)
	body, err := json.Marshal(map[string]any{
		"model": strings.TrimSpace(model),
		"content": []map[string]any{
			{"type": "text", "text": strings.TrimSpace(prompt)},
			{"type": "image_url", "image_url": map[string]string{"url": strings.TrimSpace(imageURL)}, "role": "first_frame"},
		},
		"resolution":     resolution,
		"ratio":          aspectRatio,
		"duration":       duration,
		"generate_audio": true,
		"watermark":      false,
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
		return "", httpError(p.name, "create", resp.StatusCode, raw)
	}
	var envelope struct {
		ID      string `json:"id"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidResponse, p.name+" parse create response", err)
	}
	if msg := firstNonEmpty(envelope.Message, envelope.Error.Message); msg != "" {
		if code := firstNonEmpty(envelope.Code, envelope.Error.Code); code != "" {
			return "", provider.Errorf(provider.ErrorUnavailable, "%s create: %s: %s", p.name, code, msg)
		}
		return "", provider.Errorf(provider.ErrorUnavailable, "%s create: %s", p.name, msg)
	}
	taskID := strings.TrimSpace(envelope.ID)
	if taskID == "" {
		return "", provider.New(provider.ErrorInvalidResponse, p.name+" empty task id in create response")
	}
	return taskID, nil
}

type taskPollResult struct {
	status       string
	videoURL     string
	errorCode    string
	errorMessage string
}

func (p *Provider) getTask(ctx context.Context, taskID string) (taskPollResult, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return taskPollResult{}, provider.New(provider.ErrorInvalidArgument, "task id is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+createPath+"/"+taskID, nil)
	if err != nil {
		return taskPollResult{}, provider.Wrap(provider.ErrorInvalidArgument, "create poll request", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return taskPollResult{}, ctx.Err()
		}
		return taskPollResult{}, provider.Wrap(provider.ErrorUnavailable, p.name+" poll failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return taskPollResult{}, provider.Wrap(provider.ErrorUnavailable, p.name+" read poll response", err)
	}
	if resp.StatusCode >= 400 {
		return taskPollResult{}, httpError(p.name, "query", resp.StatusCode, raw)
	}
	var envelope struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Content struct {
			VideoURL string `json:"video_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return taskPollResult{}, provider.Wrap(provider.ErrorInvalidResponse, p.name+" parse poll response", err)
	}
	if msg := firstNonEmpty(envelope.Message, envelope.Error.Message); msg != "" && !isSuccessStatus(envelope.Status) {
		return taskPollResult{
			status:       firstNonEmpty(envelope.Status, "failed"),
			errorCode:    firstNonEmpty(envelope.Code, envelope.Error.Code),
			errorMessage: msg,
		}, nil
	}
	return taskPollResult{
		status:   envelope.Status,
		videoURL: strings.TrimSpace(envelope.Content.VideoURL),
	}, nil
}

func isSuccessStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "completed", "done":
		return true
	default:
		return false
	}
}

func httpError(name, op string, status int, raw []byte) error {
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 400 {
		msg = msg[:400] + "..."
	}
	var errBody struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &errBody)
	detail := firstNonEmpty(errBody.Error.Message, errBody.Message, msg)
	kind := provider.Kind(provider.FromHTTP(name, status))
	if code := firstNonEmpty(errBody.Error.Code, errBody.Code); code != "" {
		return provider.Errorf(kind, "%s %s HTTP %d: %s: %s", name, op, status, code, detail)
	}
	return provider.Errorf(kind, "%s %s HTTP %d: %s", name, op, status, detail)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
