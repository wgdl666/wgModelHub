// Package minimaxtts 实现 Minimax WebSocket 同步 TTS。
// 在一次 unary 生命周期内完成 NewStream → SendText → Flush → 收集完整 MP3，
// 取消或失败时关闭上游，禁止把半截音频当成成功响应。
package minimaxtts

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

const (
	defaultEndpoint      = "wss://api.minimaxi.com/ws/v1/t2a_v2"
	defaultLanguageBoost = "Chinese"
	defaultVoiceID       = "Chinese (Mandarin)_Warm_Girl"
	defaultSpeed         = 1.0
	defaultVolume        = 1.0
	mimeMP3              = "audio/mpeg"
	sampleRate           = 16000
	bitrate              = 128000
	channel              = 1
	formatMP3            = "mp3"
	flushTimeout         = 30 * time.Second
	pingInterval         = 30 * time.Second
)

const (
	eventTaskStart    = "task_start"
	eventTaskContinue = "task_continue"
	eventTaskFinish   = "task_finish"

	eventConnectedSuccess = "connected_success"
	eventTaskStarted      = "task_started"
	eventTaskFinished     = "task_finished"
	eventTaskFailed       = "task_failed"
)

// Config 是 Minimax TTS 实例配置；音频格式在代码内固定，避免与线上解码假设分叉。
type Config struct {
	Name          string
	APIKey        string
	Endpoint      string
	LanguageBoost string
	VoiceID       string
	Speed         float64
	Volume        float64
	Pitch         int
}

// Provider 无会话级复用：每次 SynthesizeSpeech 独立建连，生命周期绑定调用方 ctx。
type Provider struct {
	cfg Config
}

func New(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "minimax tts api_key is required")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "minimax tts provider name is required")
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if strings.TrimSpace(cfg.LanguageBoost) == "" {
		cfg.LanguageBoost = defaultLanguageBoost
	}
	if strings.TrimSpace(cfg.VoiceID) == "" {
		cfg.VoiceID = defaultVoiceID
	}
	if cfg.Speed == 0 {
		cfg.Speed = defaultSpeed
	}
	if cfg.Volume == 0 {
		cfg.Volume = defaultVolume
	}
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) SynthesizeSpeech(ctx context.Context, model string, request *modelhubv2.SynthesizeSpeechRequest) (*modelhubv2.SynthesizeSpeechResponse, error) {
	if request == nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "synthesize speech request is required")
	}
	text := strings.TrimSpace(request.GetText())
	if text == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "text is required")
	}
	// Minimax 文档为「小于 10000 字符」；用 rune 计数对齐中文场景。
	if utf8.RuneCountInString(text) >= protocol.MaxSpeechTextChars {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "text exceeds %d characters", protocol.MaxSpeechTextChars)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "model is required")
	}

	voiceID := strings.TrimSpace(request.GetVoiceId())
	if voiceID == "" {
		voiceID = p.cfg.VoiceID
	}

	var (
		audioMu   sync.Mutex
		audio     []byte
		streamErr error
	)
	setErr := func(err error) {
		if err == nil {
			return
		}
		audioMu.Lock()
		if streamErr == nil {
			streamErr = err
		}
		audioMu.Unlock()
	}

	stream, stopWatch, err := p.newStream(ctx, model, voiceID, func(chunk []byte) {
		if len(chunk) == 0 {
			return
		}
		audioMu.Lock()
		defer audioMu.Unlock()
		if streamErr != nil {
			return
		}
		if len(audio)+len(chunk) > protocol.MaxMediaBytes {
			streamErr = provider.Errorf(provider.ErrorInvalidResponse, "speech audio exceeds %d bytes", protocol.MaxMediaBytes)
			return
		}
		audio = append(audio, chunk...)
	}, setErr)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	// stopWatch 由 newStream 在 Dial 成功后注册；整段 unary（含握手与 Flush）结束后再停，
	// 避免 AfterFunc 继续持有已结束的请求 ctx。
	defer stopWatch()

	if err := stream.SendText(text); err != nil {
		return nil, mapDialOrIO(err)
	}
	if err := stream.Flush(ctx); err != nil {
		return nil, mapDialOrIO(err)
	}

	audioMu.Lock()
	deferred := streamErr
	out := append([]byte(nil), audio...)
	audioMu.Unlock()
	if deferred != nil {
		return nil, deferred
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// 空音频不能伪装成功：调用方会拿去解码/播放，空结果属于供应商无效响应。
		return nil, provider.New(provider.ErrorInvalidResponse, "speech provider returned empty audio")
	}
	return &modelhubv2.SynthesizeSpeechResponse{
		Audio: &modelhubv2.Media{
			MimeType: mimeMP3,
			Source:   &modelhubv2.Media_Data{Data: out},
		},
	}, nil
}

type stream struct {
	conn      *websocket.Conn
	audioCb   func([]byte)
	errCb     func(error)
	closeOnce sync.Once
	doneChan  chan struct{}
	stopPing  chan struct{}
	mu        sync.Mutex
	closing   bool
	failed    bool
}

// newStream 在 WebSocket Upgrade 成功后立刻把 ctx 取消绑到关连接。
// DialContext 只覆盖 HTTP 升级；connected_success / task_started 的 ReadMessage
// 不受该截止时间控制，必须靠 Close 打断，否则 unary 会无限悬挂。
// 返回的 stopWatch 须在整次合成结束后调用；Close 经 closeOnce，可与 AfterFunc/defer 并发安全关一次。
func (p *Provider) newStream(ctx context.Context, model, voiceID string, audioCb func([]byte), errCb func(error)) (*stream, func() bool, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, p.cfg.Endpoint, header)
	if err != nil {
		if resp != nil {
			return nil, nil, provider.FromHTTP(p.cfg.Name, resp.StatusCode)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, nil, provider.Wrap(provider.ErrorUnavailable, "minimax tts dial failed", err)
	}

	s := &stream{
		conn:     conn,
		audioCb:  audioCb,
		errCb:    errCb,
		doneChan: make(chan struct{}),
		stopPing: make(chan struct{}),
	}
	// Upgrade 一成功就注册：握手卡在上游事件前时，取消/超时会关连接并唤醒 ReadMessage。
	stopWatch := context.AfterFunc(ctx, func() { s.Close() })
	if err := s.handshake(model, voiceID, p.cfg); err != nil {
		stopWatch()
		s.Close()
		// 关连接引发的读失败优先还原为调用方取消/截止语义，避免误报 Unavailable。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, ctxErr
		}
		return nil, nil, err
	}
	go s.readLoop()
	go s.pingLoop()
	return s, stopWatch, nil
}

func (s *stream) handshake(model, voiceID string, cfg Config) error {
	_, msg, err := s.conn.ReadMessage()
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "minimax tts read connected_success failed", err)
	}
	var resp wsResponse
	if err := json.Unmarshal(msg, &resp); err != nil {
		return provider.Wrap(provider.ErrorInvalidResponse, "minimax tts connected_success decode failed", err)
	}
	if resp.Event != eventConnectedSuccess {
		return provider.Errorf(provider.ErrorInvalidResponse, "minimax tts expected connected_success, got %s", resp.Event)
	}

	start := map[string]any{
		"event":          eventTaskStart,
		"model":          model,
		"language_boost": cfg.LanguageBoost,
		"voice_setting": map[string]any{
			"voice_id": voiceID,
			"speed":    cfg.Speed,
			"vol":      cfg.Volume,
			"pitch":    cfg.Pitch,
		},
		"audio_setting": map[string]any{
			"sample_rate": sampleRate,
			"bitrate":     bitrate,
			"format":      formatMP3,
			"channel":     channel,
		},
	}
	// 与 Close/SendText 共用写锁：取消路径的 AfterFunc 可能并发关流，gorilla 只允许一个 writer。
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return provider.New(provider.ErrorUnavailable, "minimax tts stream is closed")
	}
	err = s.conn.WriteJSON(start)
	s.mu.Unlock()
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "minimax tts write task_start failed", err)
	}

	_, msg, err = s.conn.ReadMessage()
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "minimax tts read task_started failed", err)
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		return provider.Wrap(provider.ErrorInvalidResponse, "minimax tts task_started decode failed", err)
	}
	if resp.Event == eventTaskFailed {
		return provider.Errorf(provider.ErrorUnavailable, "minimax tts task_start failed: %s", resp.statusMessage())
	}
	if resp.Event != eventTaskStarted {
		return provider.Errorf(provider.ErrorInvalidResponse, "minimax tts expected task_started, got %s", resp.Event)
	}
	return nil
}

func (s *stream) SendText(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.failed {
		return provider.New(provider.ErrorUnavailable, "minimax tts stream is closed")
	}
	return s.conn.WriteJSON(map[string]string{
		"event": eventTaskContinue,
		"text":  text,
	})
}

func (s *stream) Flush(ctx context.Context) error {
	s.mu.Lock()
	if s.closing || s.failed {
		s.mu.Unlock()
		return provider.New(provider.ErrorUnavailable, "minimax tts stream is closed")
	}
	err := s.conn.WriteJSON(map[string]string{"event": eventTaskFinish})
	s.mu.Unlock()
	if err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "minimax tts write task_finish failed", err)
	}

	timer := time.NewTimer(flushTimeout)
	defer timer.Stop()
	select {
	case <-s.doneChan:
		return nil
	case <-ctx.Done():
		s.Close()
		return ctx.Err()
	case <-timer.C:
		s.Close()
		return provider.New(provider.ErrorTimeout, "minimax tts flush timed out")
	}
}

func (s *stream) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		_ = s.conn.WriteJSON(map[string]string{"event": eventTaskFinish})
		s.mu.Unlock()
		close(s.stopPing)
		_ = s.conn.Close()
	})
}

func (s *stream) readLoop() {
	defer close(s.doneChan)
	for {
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			if s.isClosing() {
				return
			}
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) && !errors.Is(err, io.EOF) {
				s.fail(provider.Wrap(provider.ErrorUnavailable, "minimax tts read failed", err))
			}
			return
		}
		var resp wsResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			s.fail(provider.Wrap(provider.ErrorInvalidResponse, "minimax tts response decode failed", err))
			return
		}
		if resp.Data != nil {
			if audioHex, ok := resp.Data["audio"].(string); ok && audioHex != "" {
				data, decErr := hex.DecodeString(audioHex)
				if decErr != nil {
					s.fail(provider.Wrap(provider.ErrorInvalidResponse, "minimax tts audio hex decode failed", decErr))
					return
				}
				if s.audioCb != nil {
					s.audioCb(data)
				}
				continue
			}
		}
		switch resp.Event {
		case eventTaskFinished:
			return
		case eventTaskFailed:
			s.fail(provider.Errorf(provider.ErrorUnavailable, "minimax tts task failed: %s", resp.statusMessage()))
			return
		}
	}
}

func (s *stream) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopPing:
			return
		case <-s.doneChan:
			return
		case <-ticker.C:
			s.mu.Lock()
			if s.closing {
				s.mu.Unlock()
				return
			}
			err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (s *stream) fail(err error) {
	s.mu.Lock()
	s.failed = true
	s.mu.Unlock()
	if s.errCb != nil {
		s.errCb(err)
	}
}

func (s *stream) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

type wsResponse struct {
	Event    string                 `json:"event"`
	Data     map[string]interface{} `json:"data,omitempty"`
	BaseResp *struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp,omitempty"`
}

func (r wsResponse) statusMessage() string {
	if r.BaseResp != nil && r.BaseResp.StatusMsg != "" {
		return r.BaseResp.StatusMsg
	}
	return "unknown"
}

func mapDialOrIO(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		return err
	}
	return provider.Wrap(provider.ErrorUnavailable, "minimax tts io failed", err)
}
