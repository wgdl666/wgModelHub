package assemblyai

import (
	"bytes"
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
		BuildFinalize:      common.JSONControl("ForceEndpoint"),
		BuildStop:          common.JSONControl("Terminate"),
		Parse:              parse,
		SessionExpiredCode: "3008",
		SupportsSpeakers:   true,
		SupportsFormatted:  true,
	}), nil
}

func parse(_ int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error) {
	var event struct {
		Type            string   `json:"type"`
		Transcript      string   `json:"transcript"`
		EndOfTurn       bool     `json:"end_of_turn"`
		TurnIsFormatted bool     `json:"turn_is_formatted"`
		SpeakerLabel    string   `json:"speaker_label"`
		Confidence      float64  `json:"end_of_turn_confidence"`
		ErrorCode       jsonCode `json:"error_code"`
		ErrorText       string   `json:"error"`
		Words           []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"words"`
	}
	if err := common.ParseJSON(payload, &event); err != nil {
		return nil, err
	}
	// 账号类拒绝先发 Error 再关连接，关闭原因经常只是占位句。丢掉这帧就只剩 1008，告警看不到码。
	if event.Type == "Error" {
		code := strings.TrimSpace(string(event.ErrorCode))
		if code == "" || code == "0" {
			code = "unknown"
		}
		return nil, &common.SessionFailure{Code: code, Reason: strings.TrimSpace(event.ErrorText)}
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

// jsonCode 同时接受 "1008" 和 1008。厂家把数字码写成数字时，按字符串解会整帧失败。
type jsonCode string

func (c *jsonCode) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = jsonCode(s)
		return nil
	}
	*c = jsonCode(b)
	return nil
}
