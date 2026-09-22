// Package elevenlabstts 实现 ElevenLabs HTTP 同步 TTS。
// 在一次 unary 生命周期内拉取完整 MP3；失败不得返回半截音频。
package elevenlabstts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	defaultBaseURL = "https://api.elevenlabs.io"
	// mp3_22050_32 是 ElevenLabs 可公开拿到的最低延迟 MP3 档；无 mp3_16000。
	// Hub 侧解码后会重采样到 Mirror 的 16kHz PCM，故在此固定该封装，避免调用方再绕一层供应商参数。
	defaultOutputFormat = "mp3_22050_32"
	mimeMP3             = "audio/mpeg"
	httpTimeout         = 30 * time.Second
)

// Config 是 ElevenLabs TTS 实例配置；默认音色只来自部署配置，不在业务代码写死唯一路径。
type Config struct {
	Name    string
	APIKey  string
	BaseURL string
	VoiceID string
}

// Provider 无会话级复用：每次 SynthesizeSpeech 独立 HTTP 请求，生命周期绑定调用方 ctx。
type Provider struct {
	cfg        Config
	httpClient *http.Client
}

func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "elevenlabs tts api_key is required")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "elevenlabs tts provider name is required")
	}
	if strings.TrimSpace(cfg.VoiceID) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "elevenlabs tts voice_id is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = defaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return &Provider{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: httpTimeout,
		},
	}, nil
}

func (p *Provider) SynthesizeSpeech(ctx context.Context, model string, request *modelhubv2.SynthesizeSpeechRequest) (*modelhubv2.SynthesizeSpeechResponse, error) {
	if request == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "synthesize speech request is required")
	}
	text := strings.TrimSpace(request.GetText())
	if text == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "text is required")
	}
	if utf8.RuneCountInString(text) >= protocol.MaxSpeechTextChars {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "text exceeds %d characters", protocol.MaxSpeechTextChars)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "model is required")
	}
	// 镜子口播只接受含中文的低延迟/多语种型号；拒绝英文专用型号，避免路演中文口播静默劣化。
	if model != models.ElevenFlashV25 && model != "eleven_multilingual_v2" {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "unsupported elevenlabs speech model %q", model)
	}

	voiceID := strings.TrimSpace(request.GetVoiceId())
	if voiceID == "" {
		voiceID = p.cfg.VoiceID
	}

	body, err := json.Marshal(map[string]string{
		"text":     text,
		"model_id": model,
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, "elevenlabs tts marshal request failed", err)
	}

	url := fmt.Sprintf("%s/v1/text-to-speech/%s?output_format=%s", p.cfg.BaseURL, voiceID, defaultOutputFormat)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "elevenlabs tts build request failed", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")
	req.Header.Set("xi-api-key", p.cfg.APIKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, "elevenlabs tts request failed", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, int64(protocol.MaxMediaBytes)+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, "elevenlabs tts read body failed", err)
	}
	if len(payload) > protocol.MaxMediaBytes {
		return nil, provider.Errorf(provider.ErrorInvalidResponse, "speech audio exceeds %d bytes", protocol.MaxMediaBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// ElevenLabs 拒因在 JSON detail，截断后进入 status；这里不是音频正文。
		return nil, provider.FromHTTPDetail(p.cfg.Name, resp.StatusCode, string(payload))
	}
	if len(payload) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "speech provider returned empty audio")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &modelhubv2.SynthesizeSpeechResponse{
		Audio: &modelhubv2.Media{
			MimeType: mimeMP3,
			Source:   &modelhubv2.Media_Data{Data: payload},
		},
	}, nil
}
