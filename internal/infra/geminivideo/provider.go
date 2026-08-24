package geminivideo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	defaultBaseURL      = "https://generativelanguage.googleapis.com/v1beta"
	defaultPollInterval = 5 * time.Second
	defaultHTTPTimeout  = 10 * time.Minute
	videoMIMEType       = "video/mp4"
	silentVideoSuffix   = " Silent video. No audio, no music, no dialogue, no sound effects."
)

type Provider struct {
	name         string
	apiKey       string
	baseURL      string
	authHeader   string
	pollInterval time.Duration
	// maxPollTime 只约束迁移期同步 GenerateVideo 的前台轮询总时长，恢复原先 HTTP client 10 分钟上限语义；异步 Submit/Get 不用它。
	maxPollTime time.Duration
	client      *http.Client
}

func New(name, apiKey, baseURL, authHeader string, pollInterval float64) (*Provider, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, name+" api_key is required")
	}
	if authHeader = strings.TrimSpace(authHeader); authHeader == "" {
		authHeader = "x-goog-api-key"
	}
	base := normalizeBaseURL(baseURL)
	if pollInterval <= 0 {
		pollInterval = float64(defaultPollInterval) / float64(time.Second)
	}
	client := telemetry.NewHTTPClient()
	client.Timeout = defaultHTTPTimeout
	return &Provider{
		name:         name,
		apiKey:       apiKey,
		baseURL:      base,
		authHeader:   authHeader,
		pollInterval: time.Duration(pollInterval * float64(time.Second)),
		maxPollTime:  defaultHTTPTimeout,
		client:       client,
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

// SubmitVideo 创建 background interaction 并立即返回 id，不在 Submit 阶段等待成片。
func (p *Provider) SubmitVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (string, error) {
	if request == nil {
		return "", provider.New(provider.ErrorInvalidArgument, "video request is required")
	}
	if strings.TrimSpace(model) == "" {
		model = models.GeminiOmniFlashPreview
	}
	prompt := augmentPromptForSilentVideo(provider.JoinedText(request.GetInput()))
	if strings.TrimSpace(provider.JoinedText(request.GetInput())) == "" {
		return "", provider.New(provider.ErrorInvalidArgument, "video prompt text is required in input")
	}
	isEdit := provider.FirstVideoMedia(request.GetInput()) != nil
	var body []byte
	var err error
	if isEdit {
		body, err = p.buildEditBody(ctx, model, request, prompt)
	} else {
		videoOut := request.GetOutput().GetVideo()
		aspectRatio := ""
		duration := 0
		if videoOut != nil {
			aspectRatio = videoOut.GetAspectRatio()
			if videoOut.DurationSeconds != nil {
				duration = int(videoOut.GetDurationSeconds())
			}
		}
		body, err = p.buildI2VBody(ctx, model, request, prompt, aspectRatio, duration)
	}
	if err != nil {
		return "", err
	}
	return p.createInteraction(ctx, body)
}

// GetVideo 单次 GET interaction；completed/succeeded 视为成功，失败态映射 Failed。
func (p *Provider) GetVideo(ctx context.Context, _ string, providerTaskID string) (provider.VideoJob, error) {
	interaction, err := p.fetchInteraction(ctx, providerTaskID)
	if err != nil {
		return provider.VideoJob{}, err
	}
	pollMs := int32(p.pollInterval / time.Millisecond)
	if errMsg := interaction.errorMessage(); errMsg != "" && isInteractionFailed(interaction.Status) {
		code := ""
		if interaction.Error != nil {
			code = strings.TrimSpace(interaction.Error.Code)
		}
		var jobErr error
		if code != "" {
			jobErr = provider.Errorf(provider.ErrorUnavailable, "%s interaction %s: %s: %s", p.name, interaction.Status, code, errMsg)
		} else {
			jobErr = provider.Errorf(provider.ErrorUnavailable, "%s interaction %s: %s", p.name, interaction.Status, errMsg)
		}
		return provider.VideoJob{State: provider.VideoJobFailed, PollAfterMs: pollMs, Err: jobErr}, nil
	}
	switch strings.ToLower(strings.TrimSpace(interaction.Status)) {
	case "completed", "succeeded", "success":
		return provider.VideoJob{State: provider.VideoJobSucceeded, PollAfterMs: pollMs}, nil
	case "failed", "error", "cancelled", "canceled":
		msg := interaction.errorMessage()
		if msg == "" {
			msg = "interaction status " + interaction.Status
		}
		return provider.VideoJob{
			State:       provider.VideoJobFailed,
			PollAfterMs: pollMs,
			Err:         provider.Errorf(provider.ErrorUnavailable, "%s interaction %s: %s", p.name, interaction.Status, msg),
		}, nil
	default:
		return provider.VideoJob{State: provider.VideoJobRunning, PollAfterMs: pollMs}, nil
	}
}

// ReadVideoResult 只接受 delivery=uri 的成片：限量落盘、去音轨后流式分块；不接受 inline base64（会整段占内存）。
func (p *Provider) ReadVideoResult(ctx context.Context, _ string, providerTaskID string, emit provider.EmitEvent) error {
	interaction, err := p.fetchInteraction(ctx, providerTaskID)
	if err != nil {
		return err
	}
	videoRef := interaction.videoOutput()
	if videoRef == nil {
		return provider.New(provider.ErrorInvalidResponse, p.name+" interaction returned no video")
	}
	genElapsed := interactionGenerateElapsedMS(interaction.Created, interaction.Updated)
	mimeType := videoRef.MIMEType
	if mimeType == "" {
		mimeType = videoMIMEType
	}
	uri := strings.TrimSpace(videoRef.URI)
	if uri == "" {
		// 请求固定 delivery=uri；缺 URI 视为供应商违约，不做 inline base64 兼容解析。
		return provider.New(provider.ErrorInvalidResponse, p.name+" interaction video missing uri (delivery=uri required)")
	}
	return p.readVideoFromURI(ctx, uri, mimeType, providerTaskID, genElapsed, emit)
}

func (p *Provider) buildI2VBody(ctx context.Context, model string, request *modelhubv2.GenerateRequest, prompt, aspectRatio string, duration int) ([]byte, error) {
	imageMedia := provider.FirstImageMedia(request.GetInput())
	if imageMedia == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "first_frame image is required in input")
	}
	imageBytes, mimeType, err := p.loadMediaBytes(ctx, imageMedia, protocol.MaxMediaBytes)
	if err != nil {
		return nil, err
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	return json.Marshal(map[string]any{
		"model": model,
		"input": []map[string]any{
			{"type": "image", "data": base64.StdEncoding.EncodeToString(imageBytes), "mime_type": mimeType},
			{"type": "text", "text": prompt},
		},
		"generation_config": map[string]any{
			"video_config": map[string]string{"task": "image_to_video"},
		},
		"response_format": map[string]string{
			"type":         "video",
			"delivery":     "uri",
			"aspect_ratio": normalizeAspectRatio(aspectRatio),
			"duration":     formatDuration(duration),
		},
		"background": true,
		"store":      true,
		"stream":     false,
	})
}

func (p *Provider) buildEditBody(ctx context.Context, model string, request *modelhubv2.GenerateRequest, prompt string) ([]byte, error) {
	videoMedia := provider.FirstVideoMedia(request.GetInput())
	if videoMedia == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "video is required in input for edit")
	}
	refImages := provider.ImageMedias(request.GetInput())
	if len(refImages) == 0 {
		return nil, provider.New(provider.ErrorInvalidArgument, "reference image is required in input for edit")
	}
	videoBytes, videoMIME, err := p.loadMediaBytes(ctx, videoMedia, protocol.MaxVideoBytes)
	if err != nil {
		return nil, err
	}
	if videoMIME == "" {
		videoMIME = videoMIMEType
	}
	videoURI, err := p.uploadVideoToFiles(ctx, videoBytes, videoMIME)
	if err != nil {
		return nil, err
	}
	refBytes, refMIME, err := p.loadMediaBytes(ctx, refImages[0], protocol.MaxMediaBytes)
	if err != nil {
		return nil, err
	}
	if refMIME == "" {
		refMIME = "image/jpeg"
	}
	inputParts := []map[string]any{
		{"type": "video", "uri": videoURI, "mime_type": videoMIME},
		{"type": "image", "data": base64.StdEncoding.EncodeToString(refBytes), "mime_type": refMIME},
		{"type": "text", "text": prompt},
	}
	return json.Marshal(map[string]any{
		"model": model,
		"input": inputParts,
		"generation_config": map[string]any{
			"video_config": map[string]string{"task": "edit"},
		},
		"response_format": map[string]string{
			"type":     "video",
			"delivery": "uri",
		},
		"background": true,
		"store":      true,
		"stream":     false,
	})
}

func (p *Provider) createInteraction(ctx context.Context, body []byte) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.interactionsURL(), bytes.NewReader(body))
	if err != nil {
		return "", provider.Wrap(provider.ErrorInvalidArgument, "create interaction request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(p.authHeader, p.apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", provider.Wrap(provider.ErrorUnavailable, p.name+" interaction failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", provider.Wrap(provider.ErrorUnavailable, p.name+" read interaction response", err)
	}
	if resp.StatusCode >= 400 {
		return "", provider.FromHTTP(p.name, resp.StatusCode)
	}
	var interaction interactionResponse
	if err := json.Unmarshal(raw, &interaction); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode interaction response", err)
	}
	interactionID := strings.TrimSpace(interaction.ID)
	if interactionID == "" {
		return "", provider.New(provider.ErrorInvalidResponse, p.name+" interaction returned no id")
	}
	if errMsg := interaction.errorMessage(); errMsg != "" && isInteractionFailed(interaction.Status) {
		code := ""
		if interaction.Error != nil {
			code = strings.TrimSpace(interaction.Error.Code)
		}
		if code != "" {
			return "", provider.Errorf(provider.ErrorUnavailable, "%s interaction %s: %s: %s", p.name, interaction.Status, code, errMsg)
		}
		return "", provider.Errorf(provider.ErrorUnavailable, "%s interaction %s: %s", p.name, interaction.Status, errMsg)
	}
	return interactionID, nil
}

func (p *Provider) fetchInteraction(ctx context.Context, interactionID string) (interactionResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.interactionURL(interactionID), nil)
	if err != nil {
		return interactionResponse{}, provider.Wrap(provider.ErrorInvalidArgument, "create interaction poll request", err)
	}
	httpReq.Header.Set(p.authHeader, p.apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return interactionResponse{}, ctx.Err()
		}
		return interactionResponse{}, provider.Wrap(provider.ErrorUnavailable, p.name+" interaction poll failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return interactionResponse{}, provider.Wrap(provider.ErrorUnavailable, p.name+" read interaction poll response", err)
	}
	if resp.StatusCode >= 400 {
		return interactionResponse{}, provider.FromHTTP(p.name, resp.StatusCode)
	}
	var interaction interactionResponse
	if err := json.Unmarshal(raw, &interaction); err != nil {
		return interactionResponse{}, provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode interaction poll response", err)
	}
	return interaction, nil
}

func (p *Provider) readVideoFromURI(ctx context.Context, uri, mimeType, responseID string, genElapsed int64, emit provider.EmitEvent) error {
	inPath, cleanup, err := p.streamDownloadVideoURI(ctx, uri)
	if err != nil {
		return err
	}
	defer cleanup()
	outPath, outCleanup, err := stripAudioFromMP4File(ctx, inPath)
	if err != nil {
		return err
	}
	if outPath != inPath {
		defer outCleanup()
	}
	file, err := os.Open(outPath)
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "open stripped video", err)
	}
	defer file.Close()
	return provider.EmitVideoChunksFromReader(file, mimeType, responseID, genElapsed, emit)
}

// streamDownloadVideoURI 带鉴权流式落盘并限量，供 ReadVideoResult 去音轨前使用。
func (p *Provider) streamDownloadVideoURI(ctx context.Context, uri string) (path string, cleanup func(), err error) {
	fileID := fileIDFromURI(uri)
	if fileID != "" {
		if err := p.waitFileActive(ctx, fileID); err != nil {
			return "", nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set(p.authHeader, p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", nil, provider.Wrap(provider.ErrorUnavailable, "download video", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "gemini-video-in-*.mp4")
	if err != nil {
		return "", nil, provider.Wrap(provider.ErrorUnavailable, "create temp input", err)
	}
	cleanup = func() { _ = os.Remove(tmp.Name()) }
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, int64(protocol.MaxVideoBytes)+1))
	closeErr := tmp.Close()
	if err != nil {
		cleanup()
		return "", nil, provider.Wrap(provider.ErrorUnavailable, "stream video to temp", err)
	}
	if closeErr != nil {
		cleanup()
		return "", nil, closeErr
	}
	if written > int64(protocol.MaxVideoBytes) {
		cleanup()
		return "", nil, provider.Errorf(provider.ErrorInvalidResponse, "%s video exceeds %d bytes", p.name, protocol.MaxVideoBytes)
	}
	if written == 0 {
		cleanup()
		return "", nil, provider.New(provider.ErrorInvalidResponse, "video download returned 0 bytes")
	}
	return tmp.Name(), cleanup, nil
}

// loadMediaBytes 由调用方传入上限：编辑源视频 MaxVideoBytes，参考图/首帧 MaxMediaBytes。
func (p *Provider) loadMediaBytes(ctx context.Context, media *modelhubv2.Media, maxBytes int) ([]byte, string, error) {
	if media == nil {
		return nil, "", provider.New(provider.ErrorInvalidArgument, "media is required")
	}
	mimeType := strings.TrimSpace(media.GetMimeType())
	if data, ok := media.Source.(*modelhubv2.Media_Data); ok {
		if len(data.Data) == 0 {
			return nil, "", provider.New(provider.ErrorInvalidArgument, "media data is empty")
		}
		if len(data.Data) > maxBytes {
			return nil, "", provider.Errorf(provider.ErrorInvalidArgument, "media exceeds %d bytes", maxBytes)
		}
		return append([]byte(nil), data.Data...), mimeType, nil
	}
	if uri := provider.MediaURI(media); uri != "" {
		data, err := provider.DownloadPublicURL(ctx, p.client, p.name, uri, maxBytes)
		if err != nil {
			return nil, "", err
		}
		return data, mimeType, nil
	}
	return nil, "", provider.New(provider.ErrorInvalidArgument, "media source is required")
}

func (p *Provider) interactionsURL() string {
	return p.baseURL + "/interactions"
}

func (p *Provider) interactionURL(id string) string {
	return p.interactionsURL() + "/" + strings.TrimSpace(id)
}

func (p *Provider) uploadBaseURL() string {
	return strings.Replace(p.baseURL, "/v1beta", "/upload/v1beta", 1) + "/files"
}

func (p *Provider) uploadVideoToFiles(ctx context.Context, videoBytes []byte, mime string) (string, error) {
	if len(videoBytes) == 0 {
		return "", provider.New(provider.ErrorInvalidArgument, "empty video bytes")
	}
	if mime == "" {
		mime = videoMIMEType
	}
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.uploadBaseURL(), bytes.NewReader([]byte(`{"file":{"display_name":"video_edit_source"}}`)))
	if err != nil {
		return "", err
	}
	startReq.Header.Set(p.authHeader, p.apiKey)
	startReq.Header.Set("X-Goog-Upload-Protocol", "resumable")
	startReq.Header.Set("X-Goog-Upload-Command", "start")
	startReq.Header.Set("X-Goog-Upload-Header-Content-Length", fmt.Sprintf("%d", len(videoBytes)))
	startReq.Header.Set("X-Goog-Upload-Header-Content-Type", mime)
	startReq.Header.Set("Content-Type", "application/json")
	startResp, err := p.client.Do(startReq)
	if err != nil {
		return "", provider.Wrap(provider.ErrorUnavailable, "files upload start", err)
	}
	uploadURL := startResp.Header.Get("X-Goog-Upload-URL")
	startResp.Body.Close()
	if startResp.StatusCode >= 400 || uploadURL == "" {
		return "", provider.Errorf(provider.ErrorUnavailable, "%s files upload start failed: status=%d", p.name, startResp.StatusCode)
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(videoBytes))
	if err != nil {
		return "", err
	}
	upReq.Header.Set(p.authHeader, p.apiKey)
	upReq.Header.Set("X-Goog-Upload-Offset", "0")
	upReq.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	upResp, err := p.client.Do(upReq)
	if err != nil {
		return "", provider.Wrap(provider.ErrorUnavailable, "files upload finalize", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(upResp.Body, 1<<20))
	upResp.Body.Close()
	if upResp.StatusCode >= 400 {
		return "", provider.FromHTTP(p.name, upResp.StatusCode)
	}
	var fileResp struct {
		File struct {
			Name string `json:"name"`
			URI  string `json:"uri"`
		} `json:"file"`
	}
	if err := json.Unmarshal(raw, &fileResp); err != nil {
		return "", provider.Wrap(provider.ErrorInvalidResponse, "parse file upload response", err)
	}
	if fileResp.File.URI == "" {
		return "", provider.New(provider.ErrorInvalidResponse, "empty uploaded file uri")
	}
	fileID := strings.TrimPrefix(fileResp.File.Name, "files/")
	if fileID != "" {
		if err := p.waitFileActive(ctx, fileID); err != nil {
			return "", err
		}
	}
	return fileResp.File.URI, nil
}

// waitFileActive 编辑上传与 URI 输出下载前必须等 Files 进入 ACTIVE，否则 interaction 会失败。
func (p *Provider) waitFileActive(ctx context.Context, fileID string) error {
	deadline := time.Now().Add(defaultHTTPTimeout)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/files/"+fileID, nil)
		if err != nil {
			return err
		}
		req.Header.Set(p.authHeader, p.apiKey)
		resp, err := p.client.Do(req)
		if err != nil {
			return provider.Wrap(provider.ErrorUnavailable, "poll video file", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode >= 400 {
			return provider.FromHTTP(p.name, resp.StatusCode)
		}
		var fileInfo struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(raw, &fileInfo); err != nil {
			return provider.Wrap(provider.ErrorInvalidResponse, "parse file status", err)
		}
		switch strings.ToUpper(strings.TrimSpace(fileInfo.State)) {
		case "ACTIVE":
			return nil
		case "FAILED":
			return provider.New(provider.ErrorUnavailable, p.name+" video file processing failed")
		}
		if time.Now().After(deadline) {
			return provider.Errorf(provider.ErrorTimeout, "%s timed out waiting for video file ACTIVE", p.name)
		}
		timer := time.NewTimer(p.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type videoContent struct {
	Type     string `json:"type"`
	MIMEType string `json:"mime_type"`
	// Data 仅用于反序列化识别供应商是否仍回了 inline；ReadVideoResult 故意不消费它（无生产兼容证据）。
	Data string `json:"data"`
	URI  string `json:"uri"`
}

type interactionResponse struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Error   *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	OutputVideo *videoContent `json:"output_video"`
	Steps       []struct {
		Type    string         `json:"type"`
		Content []videoContent `json:"content"`
	} `json:"steps"`
}

func (r interactionResponse) errorMessage() string {
	if r.Error != nil && strings.TrimSpace(r.Error.Message) != "" {
		if code := strings.TrimSpace(r.Error.Code); code != "" {
			return code + ": " + strings.TrimSpace(r.Error.Message)
		}
		return strings.TrimSpace(r.Error.Message)
	}
	status := strings.ToLower(strings.TrimSpace(r.Status))
	if status == "failed" || status == "error" {
		return "interaction status " + r.Status
	}
	return ""
}

func (r interactionResponse) videoOutput() *videoContent {
	if r.OutputVideo != nil {
		return r.OutputVideo
	}
	for i := len(r.Steps) - 1; i >= 0; i-- {
		step := r.Steps[i]
		if !strings.EqualFold(strings.TrimSpace(step.Type), "model_output") {
			continue
		}
		for j := len(step.Content) - 1; j >= 0; j-- {
			item := step.Content[j]
			if strings.EqualFold(strings.TrimSpace(item.Type), "video") {
				return &item
			}
		}
	}
	return nil
}

func isInteractionFailed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func interactionGenerateElapsedMS(created, updated string) int64 {
	createdAt, okCreated := parseInteractionTimestamp(created)
	updatedAt, okUpdated := parseInteractionTimestamp(updated)
	if !okCreated || !okUpdated {
		return 0
	}
	elapsed := updatedAt.Sub(createdAt)
	if elapsed <= 0 {
		return 0
	}
	return elapsed.Milliseconds()
}

func parseInteractionTimestamp(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func fileIDFromURI(uri string) string {
	uri = strings.TrimSpace(uri)
	const marker = "/files/"
	idx := strings.Index(uri, marker)
	if idx < 0 {
		return ""
	}
	rest := uri[idx+len(marker):]
	if cut := strings.IndexAny(rest, "/:?"); cut >= 0 {
		rest = rest[:cut]
	}
	return strings.TrimSpace(rest)
}

func normalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		return defaultBaseURL
	}
	if !strings.HasSuffix(base, "/v1beta") {
		base += "/v1beta"
	}
	return base
}

func normalizeAspectRatio(raw string) string {
	switch strings.TrimSpace(raw) {
	case "16:9", "9:16":
		return raw
	default:
		return "9:16"
	}
}

func normalizeDuration(seconds int) int {
	if seconds <= 0 {
		return 5
	}
	if seconds < 3 {
		return 3
	}
	if seconds > 10 {
		return 10
	}
	return seconds
}

func formatDuration(seconds int) string {
	return fmt.Sprintf("%ds", normalizeDuration(seconds))
}

func augmentPromptForSilentVideo(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return strings.TrimSpace(silentVideoSuffix)
	}
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "no audio") || strings.Contains(lower, "silent video") {
		return prompt
	}
	return prompt + silentVideoSuffix
}

// stripAudioFromMP4File 对落盘 MP4 去音轨；无 ffmpeg 时返回原路径供直接读取。
func stripAudioFromMP4File(ctx context.Context, inPath string) (outPath string, cleanup func(), err error) {
	cleanup = func() {}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return inPath, cleanup, nil
	}
	outPath = filepath.Join(filepath.Dir(inPath), "gemini-video-out-"+filepath.Base(inPath))
	cleanup = func() { _ = os.Remove(outPath) }
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", inPath, "-an", "-c:v", "copy", "-movflags", "faststart", outPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		cleanup = func() {}
		return "", cleanup, provider.Wrap(provider.ErrorUnavailable, "strip audio", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output))))
	}
	info, err := os.Stat(outPath)
	if err != nil || info.Size() == 0 {
		cleanup()
		cleanup = func() {}
		return inPath, cleanup, nil
	}
	return outPath, cleanup, nil
}

var _ provider.VideoProvider = (*Provider)(nil)
