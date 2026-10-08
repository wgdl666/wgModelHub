package soniox

import (
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	common "github.com/wgdl666/wgModelHub/internal/infra/asr"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Config struct {
	APIKey string
	URL    string
}

func New(cfg Config) (provider.ASRProvider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "soniox api_key is required")
	}
	if cfg.URL == "" {
		// Soniox 当前没有 Ohio 专用节点；海外统一走美国实时端点并由部署实测时延。
		cfg.URL = "wss://stt-rt.soniox.com/transcribe-websocket"
	}
	return common.NewWebSocketProvider(common.Dialect{
		Name: "soniox",
		URL: func(*modelhubv2.TranscribeSpeechStart) (string, error) {
			return cfg.URL, nil
		},
		BuildStart: func(start *modelhubv2.TranscribeSpeechStart) (int, []byte, error) {
			body := map[string]any{
				"api_key":                    cfg.APIKey,
				"model":                      start.GetModel(),
				"audio_format":               "pcm_s16le",
				"sample_rate":                start.GetSampleRateHz(),
				"num_channels":               start.GetChannels(),
				"language_hints":             start.GetLanguageHints(),
				"enable_speaker_diarization": start.GetSpeakerDiarization(),
				"enable_endpoint_detection":  true,
			}
			if max := start.GetTurnSilence().GetMaxMs(); max > 0 {
				body["max_endpoint_delay_ms"] = max
			}
			if terms := start.GetKeyterms(); len(terms) > 0 {
				body["context"] = map[string]any{"terms": terms}
			}
			return common.JSONMessage(body)
		},
		BuildFinalize:    common.JSONControl("finalize"),
		BuildStop:        common.EmptyBinary,
		Parse:            parse,
		SupportsSpeakers: true,
	}), nil
}

func parse(_ int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error) {
	var event struct {
		ErrorMessage string `json:"error_message"`
		Tokens       []struct {
			Text       string  `json:"text"`
			IsFinal    bool    `json:"is_final"`
			StartMs    int64   `json:"start_ms"`
			EndMs      int64   `json:"end_ms"`
			Confidence float64 `json:"confidence"`
			Speaker    string  `json:"speaker"`
		} `json:"tokens"`
	}
	if err := common.ParseJSON(payload, &event); err != nil {
		return nil, err
	}
	if event.ErrorMessage != "" {
		return nil, provider.New(provider.ErrorUnavailable, "soniox rejected streaming session: "+event.ErrorMessage)
	}
	var partial, final strings.Builder
	var firstStart, lastEnd int64
	var confidence float64
	var speaker string
	for _, token := range event.Tokens {
		if token.IsFinal {
			final.WriteString(token.Text)
		} else {
			partial.WriteString(token.Text)
		}
		if firstStart == 0 || token.StartMs < firstStart {
			firstStart = token.StartMs
		}
		if token.EndMs > lastEnd {
			lastEnd = token.EndMs
		}
		confidence = token.Confidence
		speaker = token.Speaker
	}
	text := strings.TrimSpace(final.String())
	isFinal := text != ""
	if !isFinal {
		text = strings.TrimSpace(partial.String())
	}
	if text == "" {
		return nil, nil
	}
	return []*modelhubv2.TranscribeSpeechTranscript{{
		Text:         text,
		IsFinal:      isFinal,
		HasMediaTime: lastEnd > firstStart,
		MediaStartMs: firstStart,
		MediaEndMs:   lastEnd,
		Confidence:   confidence,
		Speaker:      speaker,
	}}, nil
}
