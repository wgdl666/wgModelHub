package qwen

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	common "github.com/wgdl666/wgModelHub/internal/infra/asr"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Config struct {
	APIKey      string
	WorkspaceID string
	URL         string
}

func New(cfg Config) (provider.ASRProvider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "qwen asr api_key is required")
	}
	return common.NewWebSocketProvider(common.Dialect{
		Name: "qwen-asr",
		URL: func(start *modelhubv2.TranscribeSpeechStart) (string, error) {
			raw := strings.TrimSpace(cfg.URL)
			if raw == "" && cfg.WorkspaceID != "" {
				raw = fmt.Sprintf("wss://%s.cn-beijing.maas.aliyuncs.com/api-ws/v1/realtime", cfg.WorkspaceID)
			}
			if raw == "" {
				raw = "wss://dashscope.aliyuncs.com/api-ws/v1/realtime"
			}
			u, err := url.Parse(raw)
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("model", start.GetModel())
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		Header: func() http.Header {
			h := http.Header{}
			h.Set("Authorization", "Bearer "+cfg.APIKey)
			h.Set("OpenAI-Beta", "realtime=v1")
			if cfg.WorkspaceID != "" {
				h.Set("X-DashScope-WorkSpace", cfg.WorkspaceID)
			}
			return h
		},
		BuildStart: func(start *modelhubv2.TranscribeSpeechStart) (int, []byte, error) {
			transcription := map[string]any{}
			if len(start.GetLanguageHints()) > 0 {
				transcription["language"] = start.GetLanguageHints()[0]
			}
			if terms := start.GetKeyterms(); len(terms) > 0 {
				// Qwen 的 corpus 是纯文本上下文；只拼调用方显式热词，不带厂家 raw 选项。
				transcription["corpus"] = map[string]any{"text": strings.Join(terms, "\n")}
			}
			turn := map[string]any{"type": "server_vad"}
			if max := start.GetTurnSilence().GetMaxMs(); max > 0 {
				turn["silence_duration_ms"] = max
			}
			return common.JSONMessage(map[string]any{"type": "session.update", "session": map[string]any{
				"modalities": []string{"text"}, "input_audio_format": "pcm",
				"sample_rate": start.GetSampleRateHz(), "input_audio_transcription": transcription,
				"turn_detection": turn,
			}})
		},
		BuildAudio:    common.Base64Audio("input_audio_buffer.append"),
		BuildFinalize: common.JSONControl("input_audio_buffer.commit"),
		BuildStop:     common.JSONControl("session.finish"),
		Parse:         parse,
	}), nil
}

func parse(_ int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error) {
	var event struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		Stash      string `json:"stash"`
		Transcript string `json:"transcript"`
		Error      struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := common.ParseJSON(payload, &event); err != nil {
		return nil, err
	}
	switch event.Type {
	case "error", "conversation.item.input_audio_transcription.failed":
		return nil, fmt.Errorf("%s: %s", event.Error.Code, event.Error.Message)
	case "conversation.item.input_audio_transcription.text":
		text := strings.TrimSpace(event.Text + event.Stash)
		if text != "" {
			return []*modelhubv2.TranscribeSpeechTranscript{{Text: text}}, nil
		}
	case "conversation.item.input_audio_transcription.completed":
		if text := strings.TrimSpace(event.Transcript); text != "" {
			return []*modelhubv2.TranscribeSpeechTranscript{{Text: text, IsFinal: true}}, nil
		}
	}
	return nil, nil
}
