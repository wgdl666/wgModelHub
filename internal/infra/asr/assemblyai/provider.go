package assemblyai

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
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
		return nil, provider.New(provider.ErrorConfiguration, "assemblyai api_key is required")
	}
	if cfg.URL == "" {
		cfg.URL = "wss://streaming.assemblyai.com/v3/ws"
	}
	return common.NewWebSocketProvider(common.Dialect{
		Name: "assemblyai",
		URL: func(start *modelhubv2.TranscribeSpeechStart) (string, error) {
			u, err := url.Parse(cfg.URL)
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("sample_rate", strconv.Itoa(int(start.GetSampleRateHz())))
			q.Set("encoding", "pcm_s16le")
			q.Set("speech_model", start.GetModel())
			q.Set("format_turns", strconv.FormatBool(start.GetFeatures().GetPreferFormattedFinal()))
			if start.GetSpeakerDiarization() {
				q.Set("speaker_labels", "true")
				if max := start.GetFeatures().GetMaxSpeakers(); max > 0 {
					q.Set("max_speakers", strconv.Itoa(int(max)))
				}
			}
			if hints := start.GetLanguageHints(); len(hints) > 0 {
				raw, _ := json.Marshal(hints)
				q.Set("language_codes", string(raw))
			}
			if terms := start.GetKeyterms(); len(terms) > 0 {
				raw, _ := json.Marshal(terms)
				q.Set("keyterms_prompt", string(raw))
			}
			if silence := start.GetTurnSilence(); silence != nil {
				if silence.GetMinMs() > 0 {
					q.Set("min_turn_silence", strconv.Itoa(int(silence.GetMinMs())))
				}
				if silence.GetMaxMs() > 0 {
					q.Set("max_turn_silence", strconv.Itoa(int(silence.GetMaxMs())))
				}
			}
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		Header: func() http.Header {
			h := http.Header{}
			// AssemblyAI v3 要求原始 key；加 Bearer 会被拒绝。
			h.Set("Authorization", cfg.APIKey)
			return h
		},
		BuildFinalize:     common.JSONControl("ForceEndpoint"),
		BuildStop:         common.JSONControl("Terminate"),
		Parse:             parse,
		SupportsSpeakers:  true,
		SupportsFormatted: true,
	}), nil
}

func parse(_ int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error) {
	var event struct {
		Type            string  `json:"type"`
		Transcript      string  `json:"transcript"`
		EndOfTurn       bool    `json:"end_of_turn"`
		TurnIsFormatted bool    `json:"turn_is_formatted"`
		SpeakerLabel    string  `json:"speaker_label"`
		Confidence      float64 `json:"end_of_turn_confidence"`
		Words           []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"words"`
	}
	if err := common.ParseJSON(payload, &event); err != nil {
		return nil, err
	}
	if event.Type != "Turn" || strings.TrimSpace(event.Transcript) == "" {
		return nil, nil
	}
	out := &modelhubv2.TranscribeSpeechTranscript{
		Text:       strings.TrimSpace(event.Transcript),
		IsFinal:    event.EndOfTurn,
		Confidence: event.Confidence,
		Speaker:    strings.TrimSpace(event.SpeakerLabel),
	}
	if len(event.Words) > 0 {
		out.HasMediaTime = true
		out.MediaStartMs = int64(event.Words[0].Start)
		out.MediaEndMs = int64(event.Words[len(event.Words)-1].End)
	}
	return []*modelhubv2.TranscribeSpeechTranscript{out}, nil
}
