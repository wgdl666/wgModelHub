package ominilinkvideo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const (
	defaultBaseURL         = "https://vg-api.aig-ai.com/v1"
	defaultDownloadBaseURL = "https://download-vod.aig-ai.com"
	defaultPollInterval    = 3 * time.Second
	videoMIMEType          = "video/mp4"
)

type Provider struct {
	name         string
	apiKey       string
	baseURL      string
	pollInterval time.Duration
	client       *http.Client
}

func New(name, apiKey, baseURL string, pollInterval float64) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" api_key is required")
	}
	// YAML 省略 poll 字段时沿用 Dev 直连默认，与 Config.Validate 接受 0 的语义一致。
	if pollInterval <= 0 {
		pollInterval = float64(defaultPollInterval) / float64(time.Second)
	}
	return &Provider{
		name:         name,
		apiKey:       apiKey,
		baseURL:      normalizeBaseURL(baseURL),
		pollInterval: time.Duration(pollInterval * float64(time.Second)),
		client:       telemetry.NewHTTPClient(),
	}, nil
}

// SubmitVideo 按 family 创建 OminiLink 异步任务，task id 足以后续 getTask 查询。
func (p *Provider) SubmitVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (string, error) {
	if request == nil {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "video request is required")
	}
	if provider.FirstVideoMedia(request.GetInput()) != nil {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "ominilink video generation does not accept video input")
	}
	prompt := provider.JoinedText(request.GetInput())
	if strings.TrimSpace(prompt) == "" {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "video prompt text is required in input")
	}
	images := provider.ImageMedias(request.GetInput())
	if len(images) == 0 {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "first_frame image is required in input")
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
	return p.createTask(ctx, model, prompt, resolution, duration, aspectRatio, images)
}

// GetVideo 单次查询供应商任务；成功态缺 URL 留 ReadVideoResult 报告无效结果。
func (p *Provider) GetVideo(ctx context.Context, model, providerTaskID string) (provider.VideoJob, error) {
	res, err := p.getTask(ctx, model, providerTaskID)
	if err != nil {
		return provider.VideoJob{}, err
	}
	job := provider.VideoJob{State: res.state, PollAfterMs: int32(p.pollInterval / time.Millisecond)}
	if res.state == provider.VideoJobFailed {
		if res.errorCode != "" {
			job.Err = provider.Errorf(provider.ErrorUnavailable, "%s task failed: %s: %s", p.name, res.errorCode, res.errorMessage)
		} else {
			job.Err = provider.Errorf(provider.ErrorUnavailable, "%s task failed: %s", p.name, res.errorMessage)
		}
	}
	return job, nil
}

// ReadVideoResult 取 result_url 并流式下载；query 需 model 与 Submit 一致。
func (p *Provider) ReadVideoResult(ctx context.Context, model, providerTaskID string, emit provider.EmitEvent) error {
	res, err := p.getTask(ctx, model, providerTaskID)
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

func (p *Provider) createTask(ctx context.Context, model, prompt, resolution string, duration int, aspectRatio string, images []*modelhubv2.Media) (string, error) {
	createURL, body, err := p.buildCreateRequest(ctx, model, prompt, resolution, duration, aspectRatio, images)
	if err != nil {
		return "", err
	}
	httpReq, err := body.NewRequest(ctx, http.MethodPost, createURL)
	if err != nil {
		return "", provider.WrapNotAttempted(provider.ErrorInvalidArgument, "create submit request", err)
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
		return "", provider.FromHTTPDetail(p.name, resp.StatusCode, string(raw))
	}
	taskID, err := parseCreateResponse(model, raw)
	if err != nil {
		return "", err
	}
	if taskID == "" {
		return "", provider.New(provider.ErrorInvalidResponse, p.name+" empty task id in create response")
	}

	return taskID, nil
}

func (p *Provider) buildCreateRequest(ctx context.Context, model, prompt, resolution string, duration int, aspectRatio string, images []*modelhubv2.Media) (string, *httpbody.Body, error) {
	model = strings.TrimSpace(model)
	resolution, duration, aspectRatio = NormalizeParams(model, resolution, duration, aspectRatio)
	prompt = strings.TrimSpace(prompt)
	switch ModelFamilyOf(model) {
	case FamilySeedance:
		return p.buildSeedanceRequest(model, prompt, resolution, duration, aspectRatio, images)
	case FamilyVeo:
		return p.buildVeoRequest(ctx, model, prompt, resolution, duration, aspectRatio, images[0])
	case FamilyKling:
		return p.buildKlingRequest(model, prompt, resolution, duration, images[0])
	case FamilyVidu:
		return p.buildViduRequest(model, prompt, resolution, duration, images[0])
	default:
		return "", nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "unsupported ominilink video model %s", model)
	}
}

func parseCreateResponse(model string, raw []byte) (string, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidResponse, "parse create response", err)
	}
	switch ModelFamilyOf(model) {
	case FamilySeedance:
		return parseSeedanceSubmit(envelope)
	case FamilyVeo:
		return parseVeoSubmit(envelope)
	case FamilyKling:
		return parseKlingSubmit(envelope)
	case FamilyVidu:
		return parseViduSubmit(envelope)
	default:
		return "", provider.Errorf(provider.ErrorInvalidArgument, "unsupported ominilink video model %s", model)
	}
}

func parseTaskResponse(model string, raw []byte) (taskPollResult, error) {
	envelope, err := decodeTaskResponse(raw)
	if err != nil {
		return taskPollResult{}, err
	}
	switch ModelFamilyOf(model) {
	case FamilySeedance:
		return parseSeedanceTask(envelope), nil
	case FamilyVeo:
		return parseVeoTask(envelope), nil
	case FamilyKling:
		return parseKlingTask(envelope), nil
	case FamilyVidu:
		return parseViduTask(envelope), nil
	default:
		return taskPollResult{}, provider.Errorf(provider.ErrorInvalidArgument, "unsupported ominilink video model %s", model)
	}
}

func (p *Provider) getTask(ctx context.Context, model, taskID string) (taskPollResult, error) {
	// Submit 已成功后的轮询；create poll 失败仍记真实调用，不得 NotAttempted。
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
		return taskPollResult{}, provider.FromHTTPDetail(p.name, resp.StatusCode, string(raw))
	}
	parsed, err := parseTaskResponse(model, raw)
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

func (p *Provider) modelQueryURL(model, taskID string) string {
	if ModelFamilyOf(model) == FamilySeedance {
		return p.seedanceCreateURL() + "/" + url.PathEscape(strings.TrimSpace(taskID))
	}
	return fmt.Sprintf("%s/query/%s/%s", p.baseURL, url.PathEscape(strings.TrimSpace(model)), url.PathEscape(strings.TrimSpace(taskID)))
}

var _ provider.VideoProvider = (*Provider)(nil)
