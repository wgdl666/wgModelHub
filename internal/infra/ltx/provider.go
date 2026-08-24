package ltx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	videoMIMEType = "video/mp4"
)

type Provider struct {
	name         string
	baseURL      string
	token        string
	duration     float64
	fps          int
	seed         int
	pollInterval time.Duration
	maxPollTime  time.Duration
	client       *http.Client
}

func New(name, baseURL, token string, duration float64, fps, seed int, pollInterval, maxPollTime float64) (*Provider, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" base_url is required")
	}
	if duration <= 0 || fps <= 0 || pollInterval <= 0 || maxPollTime <= 0 {
		return nil, provider.New(provider.ErrorConfiguration, name+" LTX timing configuration is incomplete")
	}
	if seed == 0 {
		seed = 42
	}
	return &Provider{
		name:         name,
		baseURL:      strings.TrimRight(baseURL, "/"),
		token:        token,
		duration:     duration,
		fps:          fps,
		seed:         seed,
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

// SubmitVideo 加载首帧并提交 LTX /vton，返回 job_id 供后续 Get/Read 使用。
func (p *Provider) SubmitVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (string, error) {
	if request == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "video request is required")
	}
	firstFrame := provider.FirstImageMedia(request.GetInput())
	imageBytes, err := p.loadFirstFrame(ctx, firstFrame)
	if err != nil {
		return "", err
	}
	resolution := ""
	if video := request.GetOutput().GetVideo(); video != nil {
		resolution = video.GetResolution()
	}
	return p.submit(ctx, model, imageBytes, provider.JoinedText(request.GetInput()), resolution)
}

// GetVideo 单次查询 /jobs/{id}；done/error/进行中分别映射为 Succeeded/Failed/Running。
func (p *Provider) GetVideo(ctx context.Context, _ string, providerTaskID string) (provider.VideoJob, error) {
	job, err := p.getJob(ctx, providerTaskID)
	if err != nil {
		return provider.VideoJob{}, err
	}
	pollMs := int32(p.pollInterval / time.Millisecond)
	switch job["status"] {
	case "done":
		return provider.VideoJob{State: provider.VideoJobSucceeded, PollAfterMs: pollMs}, nil
	case "error":
		// 三方错误正文可能包含输入片段，只保留稳定分类，不写入 gRPC status 或遥测。
		return provider.VideoJob{
			State:       provider.VideoJobFailed,
			PollAfterMs: pollMs,
			Err:         provider.Errorf(provider.ErrorUnavailable, "%s job failed", p.name),
		}, nil
	default:
		return provider.VideoJob{State: provider.VideoJobRunning, PollAfterMs: pollMs}, nil
	}
}

// ReadVideoResult 解析 job 内 video_url 并带鉴权流式下载，按 1MiB 分块 emit。
func (p *Provider) ReadVideoResult(ctx context.Context, _ string, providerTaskID string, emit provider.EmitEvent) error {
	job, err := p.getJob(ctx, providerTaskID)
	if err != nil {
		return err
	}
	target, err := p.videoURLFromJob(job)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return provider.Wrap(provider.ErrorInvalidArgument, "create download request", err)
	}
	p.setToken(request)
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return provider.Wrap(provider.ErrorUnavailable, p.name+" download failed", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return provider.FromHTTP(p.name, response.StatusCode)
	}
	return provider.EmitVideoChunksFromReader(response.Body, videoMIMEType, providerTaskID, 0, emit)
}

func (p *Provider) loadFirstFrame(ctx context.Context, media *modelhubv2.Media) ([]byte, error) {
	if media == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "first_frame is required")
	}
	switch source := media.Source.(type) {
	case *modelhubv2.Media_Data:
		if len(source.Data) == 0 {
			return nil, provider.New(provider.ErrorInvalidArgument, "first_frame data is empty")
		}
		if len(source.Data) > protocol.MaxMediaBytes {
			return nil, provider.Errorf(provider.ErrorInvalidArgument, "first_frame exceeds %d bytes", protocol.MaxMediaBytes)
		}
		return source.Data, nil
	case *modelhubv2.Media_Uri:
		if strings.TrimSpace(source.Uri) == "" {
			return nil, provider.New(provider.ErrorInvalidArgument, "first_frame uri is empty")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.Uri, nil)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorInvalidArgument, "create first_frame request", err)
		}
		response, err := p.client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" fetch first_frame failed", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, provider.FromHTTP(p.name, response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, protocol.MaxMediaBytes+1))
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read first_frame failed", err)
		}
		if len(data) > protocol.MaxMediaBytes {
			return nil, provider.Errorf(provider.ErrorInvalidArgument, "first_frame exceeds %d bytes", protocol.MaxMediaBytes)
		}
		return data, nil
	default:
		return nil, provider.New(provider.ErrorInvalidArgument, "first_frame source is required")
	}
}

func (p *Provider) submit(ctx context.Context, model string, imageBytes []byte, prompt, resolution string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	imagePart, err := writer.CreateFormFile("image", "first_frame.png")
	if err != nil {
		return "", provider.Wrap(provider.ErrorInvalidArgument, "create multipart image", err)
	}
	if _, err := imagePart.Write(imageBytes); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidArgument, "write multipart image", err)
	}
	fields := map[string]string{
		"prompt":     prompt,
		"resolution": normalizeResolution(resolution),
		"duration":   fmt.Sprintf("%g", p.duration),
		"fps":        fmt.Sprintf("%d", p.fps),
		"seed":       fmt.Sprintf("%d", p.seed),
		"model":      model,
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return "", provider.Wrap(provider.ErrorInvalidArgument, "write multipart field", err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidArgument, "close multipart body", err)
	}
	contentType := writer.FormDataContentType()
	payload := append([]byte(nil), body.Bytes()...)
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/vton", bytes.NewReader(payload))
		if err != nil {
			return "", provider.Wrap(provider.ErrorInvalidArgument, "create submit request", err)
		}
		request.Header.Set("Content-Type", contentType)
		p.setToken(request)
		response, err := p.client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", provider.Wrap(provider.ErrorUnavailable, p.name+" submit failed", err)
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			var result struct {
				JobID string `json:"job_id"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&result)
			response.Body.Close()
			if decodeErr != nil {
				return "", provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode submit response", decodeErr)
			}
			if result.JobID == "" {
				return "", provider.New(provider.ErrorInvalidResponse, p.name+" submit returned no job_id")
			}
			return result.JobID, nil
		}
		_, _ = io.ReadAll(io.LimitReader(response.Body, 1000))
		response.Body.Close()
		// 404/429 明确未受理可重试；5xx 可能已受理，禁止自动重提。
		retryable := response.StatusCode == http.StatusNotFound ||
			response.StatusCode == http.StatusTooManyRequests
		if !retryable || attempt == 3 {
			return "", provider.FromHTTP(p.name, response.StatusCode)
		}
		timer := time.NewTimer(time.Duration(attempt) * 300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "", provider.New(provider.ErrorUnavailable, p.name+" submit exhausted retry budget")
}

// getJob 单次 GET /jobs/{id}，供 GetVideo 与 ReadVideoResult 共用，不在此层 sleep 轮询。
func (p *Provider) getJob(ctx context.Context, jobID string) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/jobs/"+jobID, nil)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, "create poll request", err)
	}
	p.setToken(request)
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" poll failed", err)
	}
	defer response.Body.Close()
	var job map[string]any
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&job)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.FromHTTP(p.name, response.StatusCode)
	}
	if decodeErr != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode poll response", decodeErr)
	}
	return job, nil
}

func (p *Provider) videoURLFromJob(job map[string]any) (string, error) {
	target, _ := job["video_url"].(string)
	if target == "" {
		return "", provider.New(provider.ErrorInvalidResponse, p.name+" job returned no video_url")
	}
	if !strings.HasPrefix(target, "http") {
		target = p.baseURL + "/" + strings.TrimLeft(target, "/")
	}
	return target, nil
}

func (p *Provider) setToken(request *http.Request) {
	if p.token != "" {
		request.Header.Set("X-Token", p.token)
	}
}

func normalizeResolution(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "480p", "720p", "1080p":
		return value
	default:
		return "720p"
	}
}

var _ provider.VideoProvider = (*Provider)(nil)
