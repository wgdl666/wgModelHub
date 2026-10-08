// Package elevenlabstts 实现 ElevenLabs 整段和流式 HTTP TTS。
// 流式首包立即转发，只有正常 EOF 才代表完整音频。
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
	// pcm_16000 是 16 kHz、16-bit、单声道裸 PCM，没有文件头，和镜子播放格式一致。
	// ElevenLabs 没有 16 kHz 的 MP3；这条不再让 Hub 解码后再重采样。
	defaultOutputFormat = "pcm_16000"
	mimePCM16k          = "audio/pcm;rate=16000"
	httpTimeout         = 30 * time.Second
)

// VoiceSettings 对应 ElevenLabs voice_settings；字段 nil 表示不覆盖该音色账号默认值。
type VoiceSettings struct {
	Stability       *float64
	SimilarityBoost *float64
	Style           *float64
	Speed           *float64
}

// Config 是 ElevenLabs TTS 实例配置；默认音色只来自部署配置，不在业务代码写死唯一路径。
type Config struct {
	Name          string
	APIKey        string
	BaseURL       string
	VoiceID       string
	VoiceSettings VoiceSettings
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

// openSpeech 统一供应商请求与状态码映射；成功响应体由调用方关闭。
func (p *Provider) openSpeech(ctx context.Context, model string, request *modelhubv2.SynthesizeSpeechRequest, streaming bool) (*http.Response, error) {
	if request == nil {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "synthesize speech request is required")
	}
	text := strings.TrimSpace(request.GetText())
	if text == "" {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "text is required")
	}
	if utf8.RuneCountInString(text) >= protocol.MaxSpeechTextChars {
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "text exceeds %d characters", protocol.MaxSpeechTextChars)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "model is required")
	}
	// 镜子口播只接受含中文的低延迟/多语种型号；拒绝英文专用型号，避免路演中文口播静默劣化。
	if model != models.ElevenFlashV25 && model != "eleven_multilingual_v2" {
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "unsupported elevenlabs speech model %q", model)
	}

	voiceID := strings.TrimSpace(request.GetVoiceId())
	if voiceID == "" {
		voiceID = p.cfg.VoiceID
	}

	// 只发 text/model；口播滑条有配置才附 voice_settings，避免空对象覆盖账号默认。
	payload := map[string]any{
		"text":     text,
		"model_id": model,
	}
	if settings, ok := p.cfg.VoiceSettings.asRequestMap(); ok {
		payload["voice_settings"] = settings
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "elevenlabs tts marshal request failed", err)
	}

	suffix := ""
	if streaming {
		suffix = "/stream"
	}
	url := fmt.Sprintf("%s/v1/text-to-speech/%s%s?output_format=%s", p.cfg.BaseURL, voiceID, suffix, defaultOutputFormat)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorUnavailable, "elevenlabs tts build request failed", err)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		detail, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, "elevenlabs error body read failed", err)
		}
		return nil, provider.FromHTTPDetail(p.cfg.Name, resp.StatusCode, string(detail))
	}
	return resp, nil
}

func (p *Provider) SynthesizeSpeech(ctx context.Context, model string, request *modelhubv2.SynthesizeSpeechRequest) (*modelhubv2.SynthesizeSpeechResponse, error) {
	resp, err := p.openSpeech(ctx, model, request, false)
	if err != nil {
		return nil, err
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
	if len(payload) == 0 {
		return nil, provider.New(provider.ErrorInvalidResponse, "speech provider returned empty audio")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &modelhubv2.SynthesizeSpeechResponse{
		Audio: &modelhubv2.Media{
			MimeType: mimePCM16k,
			Source:   &modelhubv2.Media_Data{Data: payload},
		},
	}, nil
}

func (p *Provider) SpeechMIME() string { return mimePCM16k }

// asRequestMap 只放入已配置字段；全空则不上送，让上游继续用音色 stored settings。
func (v VoiceSettings) asRequestMap() (map[string]any, bool) {
	out := make(map[string]any, 4)
	if v.Stability != nil {
		out["stability"] = *v.Stability
	}
	if v.SimilarityBoost != nil {
		out["similarity_boost"] = *v.SimilarityBoost
	}
	if v.Style != nil {
		out["style"] = *v.Style
	}
	if v.Speed != nil {
		out["speed"] = *v.Speed
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// SynthesizeSpeechStream 直接消费 /stream 响应体，不等整句结束，也不自动重试已交付的声音。
func (p *Provider) SynthesizeSpeechStream(ctx context.Context, model string, request *modelhubv2.SynthesizeSpeechRequest, emit func([]byte) error) error {
	resp, err := p.openSpeech(ctx, model, request, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	total := 0
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			total += n
			if total > protocol.MaxMediaBytes {
				return provider.New(provider.ErrorInvalidResponse, "speech audio exceeds byte limit")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(buf[:n]); err != nil {
				return err
			}
		}
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			if readErr != io.EOF {
				return provider.Wrap(provider.ErrorUnavailable, "elevenlabs stream read failed", readErr)
			}
			if total == 0 {
				return provider.New(provider.ErrorInvalidResponse, "speech provider returned empty audio")
			}
			return nil
		}
	}
}
