package deepgram

import (
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
		return nil, provider.New(provider.ErrorConfiguration, "deepgram api_key is required")
	}
	if cfg.URL == "" {
		cfg.URL = "wss://api.deepgram.com/v1/listen"
	}
	return common.NewWebSocketProvider(common.Dialect{
		Name: "deepgram",
		URL: func(start *modelhubv2.TranscribeSpeechStart) (string, error) {
			u, err := url.Parse(cfg.URL)
			if err != nil {
				return "", err
			}
			q := u.Query()
			q.Set("model", start.GetModel())
			q.Set("encoding", "linear16")
			q.Set("sample_rate", strconv.Itoa(int(start.GetSampleRateHz())))
			q.Set("channels", strconv.Itoa(int(start.GetChannels())))
			q.Set("interim_results", "true")
			q.Set("vad_events", "true")
			q.Set("punctuate", "true")
			q.Set("smart_format", "true")
			if len(start.GetLanguageHints()) > 0 {
				q.Set("language", start.GetLanguageHints()[0])
			}
			// Deepgram 的 endpointing 与 utterance_end 不是同一阶段，分别承接统一 min/max 静音语义。
			if silence := start.GetTurnSilence(); silence != nil {
				if silence.GetMinMs() > 0 {
					q.Set("endpointing", strconv.Itoa(int(silence.GetMinMs())))
				}
				if silence.GetMaxMs() > 0 {
					q.Set("utterance_end_ms", strconv.Itoa(int(silence.GetMaxMs())))
				}
			}
			if start.GetSpeakerDiarization() {
				q.Set("diarize", "true")
			}
			for _, term := range start.GetKeyterms() {
				if term = strings.TrimSpace(term); term != "" {
					q.Add("keyterm", term)
				}
			}
			u.RawQuery = q.Encode()
			return u.String(), nil
		},
		Header: func() http.Header {
			h := http.Header{}
			h.Set("Authorization", "Token "+cfg.APIKey)
			return h
		},
		BuildFinalize: common.JSONControl("Finalize"),
		BuildStop:     common.JSONControl("CloseStream"),
		Parse:         parse,
	}), nil
}

func parse(_ int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error) {
	var event struct {
		Type        string `json:"type"`
		IsFinal     bool   `json:"is_final"`
		SpeechFinal bool   `json:"speech_final"`
		Channel     struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
				Words      []struct {
					Start   float64 `json:"start"`
					End     float64 `json:"end"`
					Speaker *int    `json:"speaker"`
				} `json:"words"`
			} `json:"alternatives"`
		} `json:"channel"`
	}
	if err := common.ParseJSON(payload, &event); err != nil {
		return nil, err
	}
	if event.Type != "Results" || len(event.Channel.Alternatives) == 0 {
		return nil, nil
	}
	alt := event.Channel.Alternatives[0]
	text := strings.TrimSpace(alt.Transcript)
	if text == "" {
		return nil, nil
	}
	out := &modelhubv2.TranscribeSpeechTranscript{Text: text, IsFinal: event.SpeechFinal, Confidence: alt.Confidence}
	if len(alt.Words) > 0 {
		out.HasMediaTime = true
		out.MediaStartMs = int64(alt.Words[0].Start * 1000)
		out.MediaEndMs = int64(alt.Words[len(alt.Words)-1].End * 1000)
		seen := map[int]struct{}{}
		for _, word := range alt.Words {
			if word.Speaker != nil {
				seen[*word.Speaker] = struct{}{}
			}
		}
		for speaker := range seen {
			out.Speakers = append(out.Speakers, strconv.Itoa(speaker+1))
		}
		if len(out.Speakers) == 1 {
			out.Speaker = out.Speakers[0]
			out.Speakers = nil
		}
	}
	return []*modelhubv2.TranscribeSpeechTranscript{out}, nil
}
