package geminivideo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
	client       *http.Client
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
		client:       client,
	}, nil
}

func (p *Provider) GenerateVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) error {
	if request == nil {
		return provider.New(provider.ErrorInvalidArgument, "video request is required")
	}
	if strings.TrimSpace(model) == "" {
		model = models.GeminiOmniFlashPreview
	}
	// 未声明静音时在 prompt 末追加后缀，生成与编辑共用，避免供应商默认配乐。
	prompt := augmentPromptForSilentVideo(provider.JoinedText(request.GetInput()))
	if strings.TrimSpace(provider.JoinedText(request.GetInput())) == "" {
		return provider.New(provider.ErrorInvalidArgument, "video prompt text is required in input")
	}
	videoOut := request.GetOutput().GetVideo()
	aspectRatio := ""
	duration := 0
	if videoOut != nil {
		aspectRatio = videoOut.GetAspectRatio()
		if videoOut.DurationSeconds != nil {
			duration = int(videoOut.GetDurationSeconds())
		}
	}
	// 输入含 video 即编辑：源片走 Files 上传并等 ACTIVE；参考图仍 inline，上限 MaxMediaBytes。
	isEdit := provider.FirstVideoMedia(request.GetInput()) != nil
	if isEdit {
		return p.generateEdit(ctx, model, request, prompt, emit)
	}
	return p.generateI2V(ctx, model, request, prompt, aspectRatio, duration, emit)
}

func (p *Provider) generateI2V(ctx context.Context, model string, request *modelhubv2.GenerateRequest, prompt, aspectRatio string, duration int, emit provider.EmitEvent) error {
	imageMedia := provider.FirstImageMedia(request.GetInput())
	if imageMedia == nil {
		return provider.New(provider.ErrorInvalidArgument, "first_frame image is required in input")
	}
	imageBytes, mimeType, err := p.loadMediaBytes(ctx, imageMedia, protocol.MaxMediaBytes)
	if err != nil {
		return err
	}
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	body, err := json.Marshal(map[string]any{
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
		"background": false,
		"store":      true,
		"stream":     false,
	})
	if err != nil {
		return err
	}
	return p.postInteraction(ctx, body, emit)
}

func (p *Provider) generateEdit(ctx context.Context, model string, request *modelhubv2.GenerateRequest, prompt string, emit provider.EmitEvent) error {
	videoMedia := provider.FirstVideoMedia(request.GetInput())
	if videoMedia == nil {
		return provider.New(provider.ErrorInvalidArgument, "video is required in input for edit")
	}
	refImages := provider.ImageMedias(request.GetInput())
	if len(refImages) == 0 {
		return provider.New(provider.ErrorInvalidArgument, "reference image is required in input for edit")
	}
	videoBytes, videoMIME, err := p.loadMediaBytes(ctx, videoMedia, protocol.MaxVideoBytes)
	if err != nil {
		return err
	}
	if videoMIME == "" {
		videoMIME = videoMIMEType
	}
	videoURI, err := p.uploadVideoToFiles(ctx, videoBytes, videoMIME)
	if err != nil {
		return err
	}
	refBytes, refMIME, err := p.loadMediaBytes(ctx, refImages[0], protocol.MaxMediaBytes)
	if err != nil {
		return err
	}
	if refMIME == "" {
		refMIME = "image/jpeg"
	}
	inputParts := []map[string]any{
		{"type": "video", "uri": videoURI, "mime_type": videoMIME},
		{"type": "image", "data": base64.StdEncoding.EncodeToString(refBytes), "mime_type": refMIME},
		{"type": "text", "text": prompt},
	}
	body, err := json.Marshal(map[string]any{
		"model": model,
		"input": inputParts,
		"generation_config": map[string]any{
			"video_config": map[string]string{"task": "edit"},
		},
		"response_format": map[string]string{
			"type":     "video",
			"delivery": "uri",
		},
		"background": false,
		"store":      true,
		"stream":     false,
	})
	if err != nil {
		return err
	}
	return p.postInteraction(ctx, body, emit)
}

func (p *Provider) postInteraction(ctx context.Context, body []byte, emit provider.EmitEvent) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.interactionsURL(), bytes.NewReader(body))
	if err != nil {
		return provider.Wrap(provider.ErrorInvalidArgument, "create interaction request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(p.authHeader, p.apiKey)
	apiStart := time.Now()
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return provider.Wrap(provider.ErrorUnavailable, p.name+" interaction failed", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, p.name+" read interaction response", err)
	}
	apiElapsed := time.Since(apiStart).Milliseconds()
	if resp.StatusCode >= 400 {
		return provider.FromHTTP(p.name, resp.StatusCode)
	}
	var interaction interactionResponse
	if err := json.Unmarshal(raw, &interaction); err != nil {
		return provider.Wrap(provider.ErrorInvalidResponse, p.name+" decode interaction response", err)
	}
	if errMsg := interaction.errorMessage(); errMsg != "" {
		code := ""
		if interaction.Error != nil {
			code = strings.TrimSpace(interaction.Error.Code)
		}
		return provider.Errorf(provider.ErrorUnavailable, "%s interaction %s: %s: %s", p.name, interaction.Status, code, errMsg)
	}
	interactionID := strings.TrimSpace(interaction.ID)
	videoRef := interaction.videoOutput()
	if videoRef == nil {
		return provider.New(provider.ErrorInvalidResponse, p.name+" interaction returned no video")
	}
	genElapsed := interactionGenerateElapsedMS(interaction.Created, interaction.Updated)
	if genElapsed <= 0 {
		genElapsed = apiElapsed
	}
	videoBytes, mimeType, err := p.resolveVideoOutput(ctx, videoRef)
	if err != nil {
		return err
	}
	// 输出统一去音轨，与 Dev 直连 Gemini 的静音契约一致；无 ffmpeg 时原样返回。
	videoBytes, err = stripAudioFromMP4(ctx, videoBytes)
	if err != nil {
		return err
	}
	if mimeType == "" {
		mimeType = videoMIMEType
	}
	return provider.EmitVideoChunks(videoBytes, mimeType, interactionID, genElapsed, emit)
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

func (p *Provider) resolveVideoOutput(ctx context.Context, ref *videoContent) ([]byte, string, error) {
	if data := strings.TrimSpace(ref.Data); data != "" {
		videoBytes, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, "", provider.Wrap(provider.ErrorInvalidResponse, "decode video data", err)
		}
		return videoBytes, ref.MIMEType, nil
	}
	uri := strings.TrimSpace(ref.URI)
	if uri == "" {
		return nil, "", provider.New(provider.ErrorInvalidResponse, "empty video data and uri")
	}
	videoBytes, err := p.downloadVideoURI(ctx, uri)
	if err != nil {
		return nil, "", err
	}
	return videoBytes, ref.MIMEType, nil
}

func (p *Provider) interactionsURL() string {
	return p.baseURL + "/interactions"
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

func (p *Provider) downloadVideoURI(ctx context.Context, uri string) ([]byte, error) {
	fileID := fileIDFromURI(uri)
	if fileID != "" {
		if err := p.waitFileActive(ctx, fileID); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(p.authHeader, p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "download video", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, provider.FromHTTP(p.name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxVideoBytes+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "read video", err)
	}
	if len(data) > protocol.MaxVideoBytes {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "%s video exceeds %d bytes", p.name, protocol.MaxVideoBytes)
	}
	return data, nil
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
	Data     string `json:"data"`
	URI      string `json:"uri"`
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

func stripAudioFromMP4(ctx context.Context, data []byte) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return data, nil
	}
	inFile, err := os.CreateTemp("", "gemini-video-in-*.mp4")
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "create temp input", err)
	}
	inPath := inFile.Name()
	defer os.Remove(inPath)
	if _, err := inFile.Write(data); err != nil {
		inFile.Close()
		return nil, err
	}
	if err := inFile.Close(); err != nil {
		return nil, err
	}
	outPath := filepath.Join(filepath.Dir(inPath), "gemini-video-out-"+filepath.Base(inPath))
	defer os.Remove(outPath)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", inPath, "-an", "-c:v", "copy", "-movflags", "faststart", outPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "strip audio", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output))))
	}
	outData, err := os.ReadFile(outPath)
	if err != nil {
		return nil, err
	}
	if len(outData) == 0 {
		return data, nil
	}
	return outData, nil
}

var _ provider.VideoProvider = (*Provider)(nil)
