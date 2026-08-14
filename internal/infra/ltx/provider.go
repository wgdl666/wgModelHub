package ltx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
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

// GenerateVideo 在 ModelHub 内同步完成提交、轮询与下载，再按 1MiB 分块回传；不持久化 job，断连不会自动重提。
func (p *Provider) GenerateVideo(ctx context.Context, model string, request *modelhubv1.GenerateVideoRequest, emit provider.EmitVideoChunk) error {
	if request == nil {
		return provider.New(provider.ErrorInvalidArgument, "video request is required")
	}
	imageBytes, err := p.loadFirstFrame(ctx, request.FirstFrame)
	if err != nil {
		return err
	}
	jobID, err := p.submit(ctx, model, imageBytes, request.Prompt, request.Resolution)
	if err != nil {
		return err
	}
	job, err := p.poll(ctx, jobID)
	if err != nil {
		return err
	}
	videoBytes, err := p.download(ctx, job)
	if err != nil {
		return err
	}
	return emitVideoChunks(videoBytes, emit)
}

func (p *Provider) loadFirstFrame(ctx context.Context, media *modelhubv1.Media) ([]byte, error) {
	if media == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "first_frame is required")
	}
	switch source := media.Source.(type) {
	case *modelhubv1.Media_Data:
		if len(source.Data) == 0 {
			return nil, provider.New(provider.ErrorInvalidArgument, "first_frame data is empty")
		}
		if len(source.Data) > protocol.MaxMediaBytes {
			return nil, provider.Errorf(provider.ErrorInvalidArgument, "first_frame exceeds %d bytes", protocol.MaxMediaBytes)
		}
		return source.Data, nil
	case *modelhubv1.Media_Uri:
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
		retryable := response.StatusCode == http.StatusNotFound ||
			response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode >= http.StatusInternalServerError
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

func (p *Provider) poll(ctx context.Context, jobID string) (map[string]any, error) {
	deadline := time.Now().Add(p.maxPollTime)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, provider.Errorf(provider.ErrorTimeout, "%s job %s timed out", p.name, jobID)
		}
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
		var job map[string]any
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&job)
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, provider.FromHTTP(p.name, response.StatusCode)
		}
		if decodeErr != nil {
			return nil, provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode poll response", decodeErr)
		}
		switch job["status"] {
		case "done":
			return job, nil
		case "error":
			// 三方错误正文可能包含输入片段，只保留稳定分类，不写入 gRPC status 或遥测。
			return nil, provider.Errorf(provider.ErrorUnavailable, "%s job failed", p.name)
		}
		timer := time.NewTimer(p.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *Provider) download(ctx context.Context, job map[string]any) ([]byte, error) {
	target, _ := job["video_url"].(string)
	if target == "" {
		return nil, provider.New(provider.ErrorInvalidResponse, p.name+" job returned no video_url")
	}
	if !strings.HasPrefix(target, "http") {
		target = p.baseURL + "/" + strings.TrimLeft(target, "/")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, "create download request", err)
	}
	p.setToken(request)
	response, err := p.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" download failed", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.FromHTTP(p.name, response.StatusCode)
	}
	// 多读 1 字节用于区分“恰好 200MiB”与“超限”，避免静默截断。
	data, err := io.ReadAll(io.LimitReader(response.Body, protocol.MaxVideoBytes+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" read video failed", err)
	}
	if len(data) > protocol.MaxVideoBytes {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "%s video exceeds %d bytes", p.name, protocol.MaxVideoBytes)
	}
	return data, nil
}

func emitVideoChunks(data []byte, emit provider.EmitVideoChunk) error {
	if emit == nil {
		return nil
	}
	if len(data) == 0 {
		return emit(&modelhubv1.VideoChunk{
			Sequence: 0,
			MimeType: videoMIMEType,
			Final:    true,
		})
	}
	var sequence uint32
	for offset := 0; offset < len(data); offset += protocol.VideoChunkBytes {
		end := offset + protocol.VideoChunkBytes
		if end > len(data) {
			end = len(data)
		}
		chunk := &modelhubv1.VideoChunk{
			Sequence: sequence,
			Data:     append([]byte(nil), data[offset:end]...),
			MimeType: videoMIMEType,
			Final:    end == len(data),
		}
		if err := emit(chunk); err != nil {
			return err
		}
		sequence++
	}
	return nil
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
