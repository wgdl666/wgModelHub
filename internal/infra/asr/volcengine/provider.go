package volcengine

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	common "github.com/wgdl666/wgModelHub/internal/infra/asr"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Config struct {
	AppKey, AccessKey, APIKey, ResourceID, URL string
}
type Provider struct{ cfg Config }

func New(cfg Config) (provider.ASRProvider, error) {
	if cfg.APIKey == "" && (cfg.AppKey == "" || cfg.AccessKey == "") {
		return nil, provider.New(provider.ErrorConfiguration, "volcengine requires api_key or app_key/access_key")
	}
	if cfg.ResourceID == "" {
		cfg.ResourceID = "volc.seedasr.sauc.duration"
	}
	if cfg.URL == "" {
		cfg.URL = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel_async"
	}
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) OpenASR(ctx context.Context, model string, start *modelhubv2.TranscribeSpeechStart, emit provider.ASREmit) (provider.ASRSession, error) {
	// 火山序号属于单条 WebSocket；放在 OpenASR 闭包里，避免并发 Hub 会话共享 seq 导致协议错乱。
	seq := int32(1)
	var finalizeMu sync.Mutex
	finalized := false
	nextFrame := func(kind byte, value any) (int, []byte, error) {
		var raw []byte
		var err error
		if data, ok := value.([]byte); ok {
			raw = data
		} else {
			raw, err = json.Marshal(value)
			if err != nil {
				return 0, nil, err
			}
		}
		compressed := gzipBytes(raw)
		var frame bytes.Buffer
		frame.Write([]byte{0x11, kind<<4 | 0x01, 0x11, 0})
		_ = binary.Write(&frame, binary.BigEndian, seq)
		seq++
		_ = binary.Write(&frame, binary.BigEndian, uint32(len(compressed)))
		frame.Write(compressed)
		return websocket.BinaryMessage, frame.Bytes(), nil
	}
	dialect := common.Dialect{
		Name: "volcengine",
		URL:  func(*modelhubv2.TranscribeSpeechStart) (string, error) { return p.cfg.URL, nil },
		Header: func() http.Header {
			h := http.Header{}
			if p.cfg.APIKey != "" {
				h.Set("X-Api-Key", p.cfg.APIKey)
			} else {
				h.Set("X-Api-App-Key", p.cfg.AppKey)
				h.Set("X-Api-Access-Key", p.cfg.AccessKey)
			}
			h.Set("X-Api-Resource-Id", p.cfg.ResourceID)
			return h
		},
		BuildStart: func(start *modelhubv2.TranscribeSpeechStart) (int, []byte, error) {
			request := map[string]any{
				"model_name": "bigmodel", "enable_nonstream": true, "enable_itn": true,
				"enable_punc": true, "enable_ddc": true, "show_utterances": true, "result_type": "single",
				"enable_speaker_info": start.GetSpeakerDiarization(),
			}
			if max := start.GetTurnSilence().GetMaxMs(); max > 0 {
				request["end_window_size"] = max
			}
			if table := start.GetFeatures().GetHotwordTableId(); table != "" {
				request["corpus"] = map[string]any{"boosting_table_id": table}
			}
			return nextFrame(0x01, map[string]any{
				"user":    map[string]any{"uid": "wg-model-hub"},
				"audio":   map[string]any{"format": "pcm", "codec": "raw", "rate": start.GetSampleRateHz(), "bits": 16, "channel": start.GetChannels()},
				"request": request,
			})
		},
		BuildAudio: func(data []byte) (int, []byte, error) { return nextFrame(0x02, data) },
		BuildFinalize: func() (int, []byte, bool) {
			finalizeMu.Lock()
			defer finalizeMu.Unlock()
			if finalized {
				return 0, nil, false
			}
			finalized = true
			compressed := gzipBytes(nil)
			var frame bytes.Buffer
			// 0x23 是 audio-only + negative sequence，明确告诉火山立即结算最后一句。
			frame.Write([]byte{0x11, 0x23, 0x11, 0})
			_ = binary.Write(&frame, binary.BigEndian, -seq)
			seq++
			_ = binary.Write(&frame, binary.BigEndian, uint32(len(compressed)))
			frame.Write(compressed)
			return websocket.BinaryMessage, frame.Bytes(), true
		},
		Parse:             parse,
		SupportsHotwordID: true,
		SupportsSpeakers:  true,
	}
	dialect.BuildStop = dialect.BuildFinalize
	return common.NewWebSocketProvider(dialect).OpenASR(ctx, model, start, emit)
}

func parse(_ int, frame []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error) {
	if len(frame) < 4 {
		return nil, fmt.Errorf("short protocol frame")
	}
	headerSize := int(frame[0]&0x0f) * 4
	if headerSize > len(frame) {
		return nil, fmt.Errorf("invalid header size")
	}
	kind, flags := frame[1]>>4, frame[1]&0x0f
	compression := frame[2] & 0x0f
	payload := frame[headerSize:]
	if flags&0x01 != 0 {
		if len(payload) < 4 {
			return nil, fmt.Errorf("missing sequence")
		}
		payload = payload[4:]
	}
	if kind == 0x0f {
		if len(payload) < 8 {
			return nil, fmt.Errorf("short server error")
		}
		code := binary.BigEndian.Uint32(payload[:4])
		payload = payload[8:]
		if compression == 1 {
			payload = gunzip(payload)
		}
		return nil, fmt.Errorf("server error %d: %s", code, payload)
	}
	if kind != 0x09 || len(payload) < 4 {
		return nil, nil
	}
	payload = payload[4:]
	if compression == 1 {
		payload = gunzip(payload)
	}
	if len(payload) == 0 {
		return nil, nil
	}
	var response struct {
		Result struct {
			Text       string `json:"text"`
			Utterances []struct {
				Definite  bool   `json:"definite"`
				StartTime int64  `json:"start_time"`
				EndTime   int64  `json:"end_time"`
				Text      string `json:"text"`
				Speaker   string `json:"speaker"`
			} `json:"utterances"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	var out []*modelhubv2.TranscribeSpeechTranscript
	for _, utterance := range response.Result.Utterances {
		if text := strings.TrimSpace(utterance.Text); text != "" {
			out = append(out, &modelhubv2.TranscribeSpeechTranscript{
				Text: text, IsFinal: utterance.Definite, HasMediaTime: utterance.EndTime > utterance.StartTime,
				MediaStartMs: utterance.StartTime, MediaEndMs: utterance.EndTime, Speaker: utterance.Speaker,
			})
		}
	}
	if len(out) == 0 && strings.TrimSpace(response.Result.Text) != "" {
		out = append(out, &modelhubv2.TranscribeSpeechTranscript{Text: strings.TrimSpace(response.Result.Text)})
	}
	return out, nil
}

func gzipBytes(raw []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	_, _ = w.Write(raw)
	_ = w.Close()
	return out.Bytes()
}

func gunzip(raw []byte) []byte {
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return raw
	}
	return out
}
