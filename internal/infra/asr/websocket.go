package asr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/wgdl666/kangaroo/logs"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const writeTimeout = 5 * time.Second

// Dialect 收拢各厂 WebSocket 的握手和事件差异；统一控制面仍只使用 proto 字段。
// BuildStart/Parse 必须只做确定的厂家映射，未知 ASRFeatures 由 OpenASR 记录诊断后忽略。
type Dialect struct {
	Name              string
	URL               func(*modelhubv2.TranscribeSpeechStart) (string, error)
	Header            func() http.Header
	BuildStart        func(*modelhubv2.TranscribeSpeechStart) (messageType int, payload []byte, err error)
	BuildAudio        func([]byte) (messageType int, payload []byte, err error)
	BuildFinalize     func() (messageType int, payload []byte, ok bool)
	BuildStop         func() (messageType int, payload []byte, ok bool)
	Parse             func(messageType int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error)
	SupportsHotwordID bool
	SupportsSpeakers  bool
	SupportsFormatted bool
}

type WebSocketProvider struct {
	dialect Dialect
}

func NewWebSocketProvider(dialect Dialect) *WebSocketProvider {
	return &WebSocketProvider{dialect: dialect}
}

func (p *WebSocketProvider) OpenASR(ctx context.Context, _ string, start *modelhubv2.TranscribeSpeechStart, emit provider.ASREmit) (provider.ASRSession, error) {
	if p == nil || p.dialect.URL == nil || p.dialect.Parse == nil {
		return nil, provider.New(provider.ErrorConfiguration, "asr websocket dialect is incomplete")
	}
	LogIgnoredFeatures(ctx, p.dialect, start.GetFeatures())
	endpoint, err := p.dialect.URL(start)
	if err != nil {
		return nil, provider.New(provider.ErrorConfiguration, err.Error())
	}
	header := http.Header{}
	if p.dialect.Header != nil {
		header = p.dialect.Header()
	}
	conn, resp, err := (&websocket.Dialer{HandshakeTimeout: 10 * time.Second}).DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.dialect.Name+" websocket dial failed", err)
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &webSocketSession{
		id:      uuid.NewString(),
		name:    p.dialect.Name,
		conn:    conn,
		dialect: p.dialect,
		emit:    emit,
		ctx:     sessionCtx,
		cancel:  cancel,
		errs:    make(chan error, 1),
	}
	if p.dialect.BuildStart != nil {
		messageType, payload, buildErr := p.dialect.BuildStart(start)
		if buildErr != nil {
			_ = conn.Close()
			cancel()
			return nil, provider.New(provider.ErrorInvalidArgument, buildErr.Error())
		}
		if err := s.write(messageType, payload); err != nil {
			_ = conn.Close()
			cancel()
			return nil, provider.Wrap(provider.ErrorUnavailable, p.dialect.Name+" start failed", err)
		}
	}
	go s.readLoop()
	return s, nil
}

func LogIgnoredFeatures(ctx context.Context, dialect Dialect, features *modelhubv2.ASRFeatures) {
	if features == nil {
		return
	}
	if features.GetHotwordTableId() != "" && !dialect.SupportsHotwordID {
		logs.Default().InfoContext(ctx, "asr_feature_ignored", "provider", dialect.Name, "feature", "hotword_table_id")
	}
	if features.GetMaxSpeakers() > 0 && !dialect.SupportsSpeakers {
		logs.Default().InfoContext(ctx, "asr_feature_ignored", "provider", dialect.Name, "feature", "max_speakers")
	}
	if features.GetPreferFormattedFinal() && !dialect.SupportsFormatted {
		logs.Default().InfoContext(ctx, "asr_feature_ignored", "provider", dialect.Name, "feature", "prefer_formatted_final")
	}
}

type webSocketSession struct {
	id      string
	name    string
	conn    *websocket.Conn
	dialect Dialect
	emit    provider.ASREmit
	ctx     context.Context
	cancel  context.CancelFunc
	errs    chan error
	writeMu sync.Mutex
	stop    sync.Once
}

func (s *webSocketSession) ID() string           { return s.id }
func (s *webSocketSession) Errors() <-chan error { return s.errs }

func (s *webSocketSession) SendAudio(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	messageType, payload, err := websocket.BinaryMessage, data, error(nil)
	if s.dialect.BuildAudio != nil {
		messageType, payload, err = s.dialect.BuildAudio(data)
	}
	if err != nil {
		return err
	}
	return s.write(messageType, payload)
}

func (s *webSocketSession) Finalize() error {
	if s.dialect.BuildFinalize == nil {
		return nil
	}
	messageType, payload, ok := s.dialect.BuildFinalize()
	if !ok {
		return nil
	}
	return s.write(messageType, payload)
}

func (s *webSocketSession) Stop() error {
	var retErr error
	s.stop.Do(func() {
		if s.dialect.BuildStop != nil {
			if messageType, payload, ok := s.dialect.BuildStop(); ok {
				retErr = s.write(messageType, payload)
			}
		}
		s.cancel()
		if err := s.conn.Close(); retErr == nil && err != nil {
			retErr = err
		}
	})
	return retErr
}

func (s *webSocketSession) write(messageType int, payload []byte) error {
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	default:
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := s.conn.WriteMessage(messageType, payload); err != nil {
		return provider.Wrap(provider.ErrorUnavailable, s.name+" websocket write failed", err)
	}
	return nil
}

func (s *webSocketSession) readLoop() {
	defer close(s.errs)
	for {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			if s.ctx.Err() == nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.report(provider.Wrap(provider.ErrorUnavailable, s.name+" websocket read failed", err))
			}
			return
		}
		transcripts, err := s.dialect.Parse(messageType, payload)
		if err != nil {
			s.report(provider.Wrap(provider.ErrorInvalidResponse, s.name+" response parse failed", err))
			return
		}
		for _, transcript := range transcripts {
			if transcript == nil || transcript.GetText() == "" {
				continue
			}
			if err := s.emit(transcript); err != nil {
				s.report(err)
				return
			}
		}
	}
}

func (s *webSocketSession) report(err error) {
	select {
	case s.errs <- err:
	default:
	}
}

func JSONMessage(value any) (int, []byte, error) {
	payload, err := json.Marshal(value)
	return websocket.TextMessage, payload, err
}

func EmptyBinary() (int, []byte, bool) {
	return websocket.BinaryMessage, []byte{}, true
}

func JSONControl(kind string) func() (int, []byte, bool) {
	return func() (int, []byte, bool) {
		payload, _ := json.Marshal(map[string]any{"type": kind})
		return websocket.TextMessage, payload, true
	}
}

func Base64Audio(eventType string) func([]byte) (int, []byte, error) {
	return func(data []byte) (int, []byte, error) {
		return JSONMessage(map[string]any{"type": eventType, "audio": base64.StdEncoding.EncodeToString(data)})
	}
}

func ParseJSON(payload []byte, target any) error {
	if len(payload) == 0 {
		return errors.New("empty websocket response")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}
