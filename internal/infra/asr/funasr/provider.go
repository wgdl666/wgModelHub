package funasr

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/google/uuid"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	common "github.com/wgdl666/wgModelHub/internal/infra/asr"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Config struct {
	APIKey      string
	WorkspaceID string
	URL         string
}

type Provider struct{ cfg Config }

func New(cfg Config) (provider.ASRProvider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "fun-asr api_key is required")
	}
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) OpenASR(ctx context.Context, model string, start *modelhubv2.TranscribeSpeechStart, emit provider.ASREmit) (provider.ASRSession, error) {
	taskID := uuid.NewString()
	var finishMu sync.Mutex
	finished := false
	buildFinish := func() (int, []byte, bool) {
		finishMu.Lock()
		defer finishMu.Unlock()
		if finished {
			return 0, nil, false
		}
		finished = true
		typ, raw, _ := common.JSONMessage(map[string]any{
			"header":  map[string]any{"action": "finish-task", "task_id": taskID, "streaming": "duplex"},
			"payload": map[string]any{"input": map[string]any{}},
		})
		return typ, raw, true
	}
	dialect := common.Dialect{
		Name: "fun-asr",
		URL: func(*modelhubv2.TranscribeSpeechStart) (string, error) {
			raw := strings.TrimSpace(p.cfg.URL)
			if raw == "" && p.cfg.WorkspaceID != "" {
				raw = fmt.Sprintf("wss://%s.cn-beijing.maas.aliyuncs.com/api-ws/v1/inference", p.cfg.WorkspaceID)
			}
			if raw == "" {
				raw = "wss://dashscope.aliyuncs.com/api-ws/v1/inference"
			}
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "wss" {
				return "", fmt.Errorf("invalid fun-asr websocket URL")
			}
			return u.String(), nil
		},
		Header: func() http.Header {
			h := http.Header{}
			h.Set("Authorization", "Bearer "+p.cfg.APIKey)
			if p.cfg.WorkspaceID != "" {
				h.Set("X-DashScope-WorkSpace", p.cfg.WorkspaceID)
			}
			return h
		},
		BuildStart: func(start *modelhubv2.TranscribeSpeechStart) (int, []byte, error) {
			parameters := map[string]any{
				"format": "pcm", "sample_rate": start.GetSampleRateHz(),
				"language_hints": start.GetLanguageHints(), "heartbeat": true,
			}
			if max := start.GetTurnSilence().GetMaxMs(); max > 0 {
				parameters["max_sentence_silence"] = max
			}
			if table := start.GetFeatures().GetHotwordTableId(); table != "" {
				parameters["vocabulary_id"] = table
			} else if terms := start.GetKeyterms(); len(terms) > 0 {
				// Fun-ASR 无内联 keyterms 参数；用 vocabulary 文本仅在厂家支持时才会生效。
				parameters["vocabulary"] = terms
			}
			return common.JSONMessage(map[string]any{
				"header": map[string]any{"action": "run-task", "task_id": taskID, "streaming": "duplex"},
				"payload": map[string]any{"task_group": "audio", "task": "asr", "function": "recognition",
					"model": model, "parameters": parameters, "input": map[string]any{}},
			})
		},
		BuildFinalize:     buildFinish,
		BuildStop:         buildFinish,
		Parse:             parse,
		SupportsHotwordID: true,
	}
	return common.NewWebSocketProvider(dialect).OpenASR(ctx, model, start, emit)
}

func parse(_ int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error) {
	var event struct {
		Header struct {
			Event        string `json:"event"`
			ErrorCode    string `json:"error_code"`
			ErrorMessage string `json:"error_message"`
		} `json:"header"`
		Payload struct {
			Output struct {
				Sentence struct {
					BeginTime   int64  `json:"begin_time"`
					EndTime     int64  `json:"end_time"`
					Text        string `json:"text"`
					Heartbeat   bool   `json:"heartbeat"`
					SentenceEnd bool   `json:"sentence_end"`
				} `json:"sentence"`
			} `json:"output"`
		} `json:"payload"`
	}
	if err := common.ParseJSON(payload, &event); err != nil {
		return nil, err
	}
	if event.Header.Event == "task-failed" {
		return nil, fmt.Errorf("task failed: %s %s", event.Header.ErrorCode, event.Header.ErrorMessage)
	}
	sentence := event.Payload.Output.Sentence
	if event.Header.Event != "result-generated" || sentence.Heartbeat || strings.TrimSpace(sentence.Text) == "" {
		return nil, nil
	}
	return []*modelhubv2.TranscribeSpeechTranscript{{
		Text:         strings.TrimSpace(sentence.Text),
		IsFinal:      sentence.SentenceEnd,
		HasMediaTime: sentence.EndTime > sentence.BeginTime,
		MediaStartMs: sentence.BeginTime,
		MediaEndMs:   sentence.EndTime,
	}}, nil
}
