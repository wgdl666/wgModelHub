package ominilinkvideo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	defaultBaseURL         = "https://vg-api.aig-ai.com/v1"
	defaultDownloadBaseURL = "https://download-vod.aig-ai.com"
	defaultPollInterval    = 3 * time.Second
	defaultMaxPollTime     = 10 * time.Minute
	videoMIMEType          = "video/mp4"
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
		baseURL:      normalizeBaseURL(baseURL),
		pollInterval: time.Duration(pollInterval * float64(time.Second)),
		maxPollTime:  time.Duration(maxPollTime * float64(time.Second)),
		client:       telemetry.NewHTTPClient(),
	}, nil
}

func (p *Provider) GenerateVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) error {
	if request == nil {
		return provider.New(provider.ErrorInvalidArgument, "video request is required")
	}
	if provider.FirstVideoMedia(request.GetInput()) != nil {
		return provider.New(provider.ErrorInvalidArgument, "ominilink video generation does not accept video input")
	}
	prompt := provider.JoinedText(request.GetInput())
	if strings.TrimSpace(prompt) == "" {
		return provider.New(provider.ErrorInvalidArgument, "video prompt text is required in input")
	}
	imageURL, err := p.resolveImageURL(provider.FirstImageMedia(request.GetInput()))
	if err != nil {
		return err
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
	taskID, err := p.createTask(ctx, model, imageURL, prompt, resolution, duration, aspectRatio)
	if err != nil {
		return err
	}
	videoURL, err := p.waitResult(ctx, model, taskID)
	if err != nil {
		return err
	}
	data, err := provider.DownloadPublicURL(ctx, p.client, p.name, videoURL, protocol.MaxVideoBytes)
	if err != nil {
		return err
	}
	return provider.EmitVideoChunks(data, videoMIMEType, taskID, 0, emit)
}

func (p *Provider) resolveImageURL(media *modelhubv2.Media) (string, error) {
	if media == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "first_frame image is required in input")
	}
	if uri := provider.MediaURI(media); uri != "" {
		return uri, nil
	}
	if data, ok := media.Source.(*modelhubv2.Media_Data); ok && len(data.Data) > 0 {
		return "", provider.New(provider.ErrorInvalidArgument, "ominilink video requires image uri for async create")
	}
	return "", provider.New(provider.ErrorInvalidArgument, "image source is required")
}

func (p *Provider) createTask(ctx context.Context, model, imageURL, prompt, resolution string, duration int, aspectRatio string) (string, error) {
	createURL, body, err := p.buildCreateRequest(ctx, model, imageURL, prompt, resolution, duration, aspectRatio)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(body))
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
		return "", provider.FromHTTP(p.name, resp.StatusCode)
	}
	taskID := taskIDFromCreateResponse(raw)
	if taskID == "" {
		return "", provider.New(provider.ErrorInvalidResponse, p.name+" empty task id in create response")
	}
	return taskID, nil
}

// buildCreateRequest 按 family 组装互斥 payload：Seedance/Vidu 用 image URL；Veo 需先拉取转 base64；
// Kling 走独立 image2video 路径。各 family 轮询响应字段名不一致，由 parseTaskResponse 统一归一。
func (p *Provider) buildCreateRequest(ctx context.Context, model, imageURL, prompt, resolution string, duration int, aspectRatio string) (string, []byte, error) {
	model = strings.TrimSpace(model)
	resolution, duration, aspectRatio = NormalizeParams(model, resolution, duration, aspectRatio)
	prompt = strings.TrimSpace(prompt)
	imageURL = strings.TrimSpace(imageURL)
	switch ModelFamilyOf(model) {
	case FamilySeedance:
		body, err := json.Marshal(map[string]any{
			"model": model,
			"content": []map[string]any{
				{"type": "text", "text": prompt},
				{"type": "image_url", "image_url": map[string]string{"url": imageURL}, "role": "first_frame"},
			},
			"resolution":        resolution,
			"ratio":             aspectRatio,
			"duration":          duration,
			"generate_audio":    true,
			"watermark":         false,
			"return_last_frame": false,
		})
		return p.modelCreateURL(model), body, err
	case FamilyVeo:
		imageB64, mimeType, err := p.fetchImageBase64(ctx, imageURL)
		if err != nil {
			return "", nil, err
		}
		body, err := json.Marshal(map[string]any{
			"instances": []map[string]any{{
				"prompt": prompt,
				"image":  map[string]string{"bytesBase64Encoded": imageB64, "mimeType": mimeType},
			}},
			"parameters": map[string]any{
				"durationSeconds":  duration,
				"sampleCount":      1,
				"resolution":       veoResolutionForAPI(resolution),
				"aspectRatio":      veoAspectRatio(aspectRatio),
				"personGeneration": "allow_adult",
				"generateAudio":    false,
			},
		})
		return p.modelCreateURL(model), body, err
	case FamilyKling:
		payload := map[string]any{
			"Image":    map[string]string{"Url": imageURL},
			"Prompt":   prompt,
			"Duration": strconv.Itoa(duration),
			"Mode":     klingMode(resolution),
			"Sound":    "off",
			"LogoAdd":  0,
		}
		if version := klingAPIVersion(model); version != "" {
			payload["Model"] = version
		}
		body, err := json.Marshal(payload)
		return p.baseURL + "/" + url.PathEscape(model) + "/image2video", body, err
	case FamilyVidu:
		body, err := json.Marshal(map[string]any{
			"model":      model,
			"images":     []string{imageURL},
			"prompt":     prompt,
			"duration":   duration,
			"resolution": resolution,
			"audio":      false,
		})
		return p.baseURL + "/" + url.PathEscape(model) + "/img2video", body, err
	default:
		return "", nil, provider.Errorf(provider.ErrorInvalidArgument, "unsupported ominilink video model %s", model)
	}
}

func (p *Provider) fetchImageBase64(ctx context.Context, imageURL string) (b64, mimeType string, err error) {
	data, err := provider.DownloadPublicURL(ctx, p.client, p.name, imageURL, protocol.MaxMediaBytes)
	if err != nil {
		return "", "", err
	}
	mimeType = mimeFromURL(imageURL)
	return base64.StdEncoding.EncodeToString(data), mimeType, nil
}

func (p *Provider) waitResult(ctx context.Context, model, taskID string) (string, error) {
	deadline := time.Now().Add(p.maxPollTime)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", provider.Errorf(provider.ErrorTimeout, "%s task %s timed out", p.name, taskID)
		}
		res, err := p.getTask(ctx, model, taskID)
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(res.status)) {
		case "succeeded", "success", "completed", "done":
			if res.videoURL == "" {
				return "", provider.New(provider.ErrorInvalidResponse, p.name+" task succeeded but video_url empty")
			}
			return res.videoURL, nil
		case "failed", "failure", "fail", "error", "cancelled", "canceled", "expired":
			msg := strings.TrimSpace(res.errorMessage)
			if msg == "" {
				msg = res.status
			}
			if code := strings.TrimSpace(res.errorCode); code != "" {
				return "", provider.Errorf(provider.ErrorUnavailable, "%s task %s: %s: %s", p.name, res.status, code, msg)
			}
			return "", provider.Errorf(provider.ErrorUnavailable, "%s task %s: %s", p.name, res.status, msg)
		}
		timer := time.NewTimer(p.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", provider.Errorf(provider.ErrorTimeout, "%s task %s timed out", p.name, taskID)
			}
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

type taskPollResult struct {
	status       string
	videoURL     string
	errorCode    string
	errorMessage string
}

func (p *Provider) getTask(ctx context.Context, model, taskID string) (taskPollResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.modelQueryURL(model, taskID), nil)
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
		return taskPollResult{}, provider.FromHTTP(p.name, resp.StatusCode)
	}
	parsed, err := parseTaskResponse(raw, taskID)
	if err != nil {
		return taskPollResult{}, err
	}
	return parsed, nil
}

func normalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return defaultBaseURL
	}
	base = strings.TrimSuffix(base, "/v1beta")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

func (p *Provider) modelCreateURL(model string) string {
	return p.baseURL + "/" + url.PathEscape(strings.TrimSpace(model))
}

func (p *Provider) modelQueryURL(model, taskID string) string {
	return fmt.Sprintf("%s/query/%s/%s", p.baseURL, url.PathEscape(strings.TrimSpace(model)), url.PathEscape(strings.TrimSpace(taskID)))
}

func mimeFromURL(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, ".png"):
		return "image/png"
	case strings.Contains(lower, ".webp"):
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

func taskIDFromCreateResponse(raw []byte) string {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	if name := stringField(envelope, "name"); name != "" {
		return name
	}
	for _, key := range []string{"id", "task_id", "taskId"} {
		if v := stringField(envelope, key); v != "" {
			return v
		}
	}
	if resp, ok := envelope["Response"].(map[string]any); ok {
		if jobID := stringField(resp, "JobId"); jobID != "" {
			return jobID
		}
	}
	if data, ok := envelope["data"].(map[string]any); ok {
		for _, key := range []string{"id", "task_id", "taskId", "name"} {
			if v := stringField(data, key); v != "" {
				return v
			}
		}
	}
	return ""
}

func parseTaskResponse(raw []byte, fallbackTaskID string) (taskPollResult, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return taskPollResult{}, provider.Wrap(provider.ErrorInvalidResponse, "parse task response", err)
	}
	if code := strings.ToLower(stringField(envelope, "code")); code != "" && code != "success" && code != "0" {
		msg := firstNonEmpty(stringField(envelope, "message"), code)
		return taskPollResult{}, provider.Errorf(provider.ErrorUnavailable, "ominilink task failed: %s", msg)
	}
	if msg := apiFailureMessage(envelope); msg != "" {
		return taskPollResult{}, provider.Errorf(provider.ErrorUnavailable, "ominilink task failed: %s", msg)
	}
	taskObj := envelope
	if data, ok := envelope["data"].(map[string]any); ok && len(data) > 0 {
		taskObj = data
	} else if resp, ok := envelope["Response"].(map[string]any); ok && len(resp) > 0 {
		taskObj = resp
	}
	status := firstNonEmpty(
		stringFieldAny(taskObj, "status", "state", "task_status", "taskStatus", "Status", "State", "TaskStatus"),
		stringFieldAny(envelope, "status", "state", "task_status", "taskStatus"),
	)
	if status == "" {
		status = veoStatusFromEnvelope(envelope)
	}
	videoURL := normalizeVideoURL(firstNonEmpty(
		stringFieldAny(taskObj, "result_url", "resultUrl", "video_url", "videoUrl", "url", "URL", "path", "Path"),
		videoURLFromEnvelope(envelope),
		videoURLFromEnvelope(taskObj),
	))
	return taskPollResult{
		status:       status,
		videoURL:     videoURL,
		errorCode:    taskErrorCode(taskObj, envelope),
		errorMessage: taskErrorMessage(taskObj, envelope),
	}, nil
}

func normalizeVideoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	base := strings.TrimRight(defaultDownloadBaseURL, "/")
	if strings.HasPrefix(raw, "/") {
		return base + raw
	}
	return base + "/" + raw
}

func videoURLFromEnvelope(envelope map[string]any) string {
	for _, key := range []string{"result_url", "resultUrl", "uri", "url", "URL", "video_url", "videoUrl", "path", "Path"} {
		if u := stringField(envelope, key); u != "" {
			return u
		}
	}
	for _, containerKey := range []string{"response", "data", "result", "output", "Response", "Output"} {
		container, ok := envelope[containerKey].(map[string]any)
		if !ok {
			continue
		}
		if u := videoURLFromEnvelope(container); u != "" {
			return u
		}
	}
	return ""
}

func veoStatusFromEnvelope(envelope map[string]any) string {
	if done, ok := envelope["done"].(bool); ok {
		if done && videoURLFromEnvelope(envelope) != "" {
			return "success"
		}
		return "processing"
	}
	if errObj, ok := envelope["error"].(map[string]any); ok && len(errObj) > 0 {
		return "failed"
	}
	return ""
}

func apiFailureMessage(envelope map[string]any) string {
	if errObj, ok := envelope["error"].(map[string]any); ok {
		if msg := stringField(errObj, "message"); msg != "" {
			return msg
		}
	}
	if reason := stringField(envelope, "fail_reason"); reason != "" {
		return reason
	}
	if msg := stringField(envelope, "message"); msg != "" {
		lowerStatus := strings.ToLower(firstNonEmpty(stringFieldAny(envelope, "status", "state")))
		if lowerStatus == "failed" || lowerStatus == "failure" || lowerStatus == "error" {
			return msg
		}
	}
	return ""
}

func taskErrorCode(objs ...map[string]any) string {
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		if code := stringFieldAny(obj, "error_code", "errorCode", "code", "Code"); code != "" {
			return code
		}
	}
	return ""
}

func taskErrorMessage(objs ...map[string]any) string {
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		if msg := apiFailureMessage(obj); msg != "" {
			return msg
		}
		if msg := stringFieldAny(obj, "fail_reason", "error_message", "errorMessage"); msg != "" {
			return msg
		}
	}
	return ""
}

func stringField(obj map[string]any, key string) string {
	v, ok := obj[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func stringFieldAny(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringField(obj, key); v != "" {
			return v
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
