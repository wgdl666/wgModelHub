package ltx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
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
	client       *http.Client
	gateway      *vmindgateway.Client
}

func NewGateway(name string, gateway *vmindgateway.Client, pollInterval float64) *Provider {
	return &Provider{
		name: name, gateway: gateway, pollInterval: time.Duration(pollInterval * float64(time.Second)),
		client: telemetry.NewHTTPClient(),
	}
}

func New(name, baseURL, token string, duration float64, fps, seed int, pollInterval float64) (*Provider, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" base_url is required")
	}
	if duration <= 0 || fps <= 0 || pollInterval <= 0 {
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
		client:       telemetry.NewHTTPClient(),
	}, nil
}

// SubmitVideo 按网关登记确认视频协议；直连供应商仍使用既有模型边界。
func (p *Provider) SubmitVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (string, error) {
	if p.gateway == nil && model != models.VWorldOutfitVideo10 {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "unsupported video model: "+model)
	}
	if request == nil {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "video request is required")
	}
	motionTips, err := videoMotionTips(request.GetInput().GetModelTraits())
	if err != nil {
		return "", err
	}
	firstFrame := provider.FirstImageMedia(request.GetInput())
	imageBytes, err := p.loadFirstFrame(ctx, firstFrame)
	if err != nil {
		return "", err
	}
	if p.gateway != nil {
		// 提交按登记确认视频契约；历史任务查询仍使用已经持久化的网关任务 ID。
		kind, err := p.gateway.ModelProtocol(ctx, model)
		if err != nil {
			return "", err
		}
		if kind != "outfit_video" {
			return "", provider.NotAttemptedf(provider.ErrorInvalidArgument, "unsupported video protocol %q", kind)
		}
		mimeType := strings.TrimSpace(firstFrame.GetMimeType())
		if mimeType == "" {
			mimeType = http.DetectContentType(imageBytes)
		}
		input := map[string]any{
			"prompt": provider.JoinedText(request.GetInput()),
			"images": []vmindgateway.Image{{MIMEType: mimeType, Data: imageBytes}},
		}
		if motionTips != "" {
			input["motion_tips"] = motionTips
		}
		task, err := p.gateway.Submit(ctx, model, provider.SubmissionID(ctx), input, http.StatusAccepted)
		return task.RequestID, err
	}
	return p.submit(ctx, imageBytes, provider.JoinedText(request.GetInput()), motionTips)
}

// videoMotionTips 只读取该模型认识的独立动作参数；缺省时保持旧调用行为。
func videoMotionTips(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var traits map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &traits) != nil || traits == nil {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "video model_traits must be a JSON object with string motion_tips")
	}
	value, exists := traits["motion_tips"]
	if !exists {
		return "", nil
	}
	var motionTips string
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &motionTips) != nil {
		return "", provider.NotAttempted(provider.ErrorInvalidArgument, "video model_traits.motion_tips must be a string")
	}
	return motionTips, nil
}

// GetVideo 单次查询 /jobs/{id}；done/error/进行中分别映射为 Succeeded/Failed/Running。
func (p *Provider) GetVideo(ctx context.Context, model string, providerTaskID string) (provider.VideoJob, error) {
	// 历史已受理视频保存旧 ID；只允许查询旧任务，不能因此重新开放旧 ID 提交。
	if model == "VWorld_outfit_video_1.0" {
		model = models.VWorldOutfitVideo10
	}
	// 直连仍拒绝已下线的 ltx ID；网关任务按持久化的任务 ID 查询。
	if p.gateway == nil && model != models.VWorldOutfitVideo10 {
		return provider.VideoJob{}, provider.New(provider.ErrorInvalidArgument, "unsupported video model: "+model)
	}
	pollMs := int32(p.pollInterval / time.Millisecond)
	var state string
	if p.gateway != nil {
		task, err := p.gateway.Get(ctx, providerTaskID)
		if err != nil {
			return provider.VideoJob{}, err
		}
		state = task.State
	} else {
		job, err := p.getJob(ctx, providerTaskID)
		if err != nil {
			return provider.VideoJob{}, err
		}
		state, _ = job["status"].(string)
	}
	switch state {
	case "done", "succeeded":
		return provider.VideoJob{State: provider.VideoJobSucceeded, PollAfterMs: pollMs}, nil
	case "error", "cancelled", "interrupted", "failed":
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
func (p *Provider) ReadVideoResult(ctx context.Context, model string, providerTaskID string, emit provider.EmitEvent) error {
	// 历史成片仍需按原 task_id 下载；保持持久任务字段和幂等哈希不变。
	if model == "VWorld_outfit_video_1.0" {
		model = models.VWorldOutfitVideo10
	}
	// 直连结果遵守原模型边界；网关校验任务所属的结果对象。
	if p.gateway == nil && model != models.VWorldOutfitVideo10 {
		return provider.New(provider.ErrorInvalidArgument, "unsupported video model: "+model)
	}
	if p.gateway != nil {
		task, err := p.gateway.Get(ctx, providerTaskID)
		if err != nil {
			return err
		}
		if task.State != "succeeded" || len(task.Result.Files) != 1 {
			return provider.New(provider.ErrorInvalidResponse, "VMind video task has no successful object result")
		}
		_, reader, err := p.gateway.OpenResult(ctx, task.RequestID, task.Result.Files[0])
		if err != nil {
			return err
		}
		defer reader.Close()
		return provider.EmitVideoChunksFromReader(reader, videoMIMEType, providerTaskID, 0, emit)
	}
	job, err := p.getJob(ctx, providerTaskID)
	if err != nil {
		return err
	}
	target, err := p.videoURLFromJob(job)
	if err != nil {
		return err
	}
	// 结果下载在 Submit 成功之后；构造失败仍算已发起生成，不得标 NotAttempted 以免账本丢弃。
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
		return provider.TakeHTTPError(p.name, response.StatusCode, response.Body)
	}
	return provider.EmitVideoChunksFromReader(response.Body, videoMIMEType, providerTaskID, 0, emit)
}

func (p *Provider) loadFirstFrame(ctx context.Context, media *modelhubv2.Media) ([]byte, error) {
	if media == nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "first_frame is required")
	}
	switch source := media.Source.(type) {
	case *modelhubv2.Media_Data:
		if len(source.Data) == 0 {
			return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "first_frame data is empty")
		}
		if len(source.Data) > protocol.MaxMediaBytes {
			return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "first_frame exceeds %d bytes", protocol.MaxMediaBytes)
		}
		return source.Data, nil
	case *modelhubv2.Media_Uri:
		if strings.TrimSpace(source.Uri) == "" {
			return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "first_frame uri is empty")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.Uri, nil)
		if err != nil {
			return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "create first_frame request", err)
		}
		response, err := p.client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				// 输入准备阶段取消：包住以免裸 context.Canceled 被记成已调模型。
				return nil, provider.AsNotAttempted(ctx.Err())
			}
			return nil, provider.AsNotAttempted(provider.Wrap(provider.ErrorUnavailable, p.name+" fetch first_frame failed", err))
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, provider.AsNotAttempted(provider.TakeHTTPError(p.name, response.StatusCode, response.Body))
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, protocol.MaxMediaBytes+1))
		if err != nil {
			return nil, provider.AsNotAttempted(provider.Wrap(provider.ErrorUnavailable, p.name+" read first_frame failed", err))
		}
		if len(data) > protocol.MaxMediaBytes {
			return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "first_frame exceeds %d bytes", protocol.MaxMediaBytes)
		}
		return data, nil
	default:
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "first_frame source is required")
	}
}

func (p *Provider) submit(ctx context.Context, imageBytes []byte, prompt, motionTips string) (string, error) {
	parts := []httpbody.Part{httpbody.File("image", "first_frame.png", "application/octet-stream", imageBytes)}
	endpoint := "/vton"
	fields := map[string]string{"prompt": prompt}
	// 当前视频网关只提供 /vton；保留服务端模型与推理默认值，避免与网关规格漂移。
	if motionTips != "" {
		fields["motion_tips"] = motionTips
	}
	// 穿搭视频和生图一样：上游只认表单参数，不认 traceparent。轮询和下载不带这个字段。
	if traceID := telemetry.TraceID(ctx); traceID != "" {
		fields["trace_id"] = traceID
	}
	for name, value := range fields {
		parts = append(parts, httpbody.Field(name, value))
	}
	body, err := httpbody.Multipart(parts)
	if err != nil {
		return "", provider.WrapNotAttempted(provider.ErrorInvalidArgument, "build multipart request", err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		request, err := body.NewRequest(ctx, http.MethodPost, p.baseURL+endpoint)
		if err != nil {
			return "", provider.WrapNotAttempted(provider.ErrorInvalidArgument, "create submit request", err)
		}
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
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		response.Body.Close()
		// 404/429 明确未受理可重试；5xx 可能已受理，禁止自动重提。
		retryable := response.StatusCode == http.StatusNotFound ||
			response.StatusCode == http.StatusTooManyRequests
		if !retryable || attempt == 3 {
			return "", provider.FromHTTPDetail(p.name, response.StatusCode, string(raw))
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
	// 轮询发生在 Submit 成功之后，create poll 失败不得标整次生成为 NotAttempted。
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
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.FromHTTPDetail(p.name, response.StatusCode, string(raw))
	}
	if readErr != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read poll response", readErr)
	}
	var job map[string]any
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode poll response", err)
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

var _ provider.VideoProvider = (*Provider)(nil)
