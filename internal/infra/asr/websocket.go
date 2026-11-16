package asr

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/wgdl666/kangaroo/logs"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/asrmetric"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

const (
	writeTimeout     = 5 * time.Second
	handshakeTimeout = 10 * time.Second
	stopWaitTimeout  = 2 * time.Second
)

// Dialect 收拢各厂 WebSocket 的握手和事件差异；统一控制面仍只使用 proto 字段。
// BuildStart/Parse 必须只做确定的厂家映射，未知 ASRFeatures 由 OpenASR 记录诊断后忽略。
type Dialect struct {
	Name          string
	URL           func(*modelhubv2.TranscribeSpeechStart) (string, error)
	Header        func() http.Header
	BuildStart    func(*modelhubv2.TranscribeSpeechStart) (messageType int, payload []byte, err error)
	BuildAudio    func([]byte) (messageType int, payload []byte, err error)
	BuildFinalize func() (messageType int, payload []byte, ok bool)
	BuildStop     func() (messageType int, payload []byte, ok bool)
	ReadSession   func(messageType int, payload []byte) (*modelhubv2.TranscribeSpeechSession, error)
	// Parse 返回 io.EOF 表示供应商已确认结束，先前转写已经交付。
	Parse              func(messageType int, payload []byte) ([]*modelhubv2.TranscribeSpeechTranscript, error)
	SessionExpiredCode string
	DrainOnStop        bool
	SupportsHotwordID  bool
	SupportsSpeakers   bool
	SupportsFormatted  bool
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
	conn, resp, err := (&websocket.Dialer{HandshakeTimeout: handshakeTimeout}).DialContext(ctx, endpoint, header)
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
		done:    make(chan struct{}),
	}
	s.info = &modelhubv2.TranscribeSpeechSession{SessionId: s.id}
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
	if p.dialect.ReadSession != nil {
		// Begin 是真实就绪确认；RPC 建立和 Start 发出都不能替代它。
		stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stopCancel()
		_ = conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
		messageType, payload, readErr := conn.ReadMessage()
		kind := provider.ErrorUnavailable
		if readErr == nil {
			kind = provider.ErrorInvalidResponse
			s.info, readErr = p.dialect.ReadSession(messageType, payload)
		}
		if readErr != nil {
			counted := false
			s.noteSessionFailure(readErr, &counted)
			_ = conn.Close()
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, s.failure(kind, "session start failed", readErr)
		}
		_ = conn.SetReadDeadline(time.Time{})
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
	info    *modelhubv2.TranscribeSpeechSession
	name    string
	conn    *websocket.Conn
	dialect Dialect
	emit    provider.ASREmit
	ctx     context.Context
	cancel  context.CancelFunc
	errs    chan error
	done    chan struct{}
	readErr error
	writeMu sync.Mutex
	stop    sync.Once
}

func (s *webSocketSession) Info() *modelhubv2.TranscribeSpeechSession { return s.info }
func (s *webSocketSession) Errors() <-chan error                      { return s.errs }

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
		if retErr == nil && s.dialect.DrainOnStop {
			timer := time.NewTimer(stopWaitTimeout)
			defer timer.Stop()
			select {
			case <-s.done:
				retErr = s.readErr
			case <-s.ctx.Done():
				retErr = s.ctx.Err()
			case <-timer.C:
				retErr = provider.Wrap(provider.ErrorTimeout, s.name+" termination timed out", context.DeadlineExceeded)
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
	defer close(s.done)
	defer close(s.errs)
	// counted 保证同一次拒绝只打一个点：Error 帧先到，后面的关闭帧不再计。
	counted := false
	for {
		messageType, payload, err := s.conn.ReadMessage()
		if err != nil {
			if s.ctx.Err() == nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.noteSessionFailure(err, &counted)
				s.report(s.failure(provider.ErrorUnavailable, "websocket read failed", err))
			} else if s.ctx.Err() == nil && s.dialect.DrainOnStop {
				s.report(provider.Wrap(provider.ErrorUnavailable, s.name+" closed before Termination", err))
			}
			return
		}
		transcripts, err := s.dialect.Parse(messageType, payload)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			s.noteSessionFailure(err, &counted)
			s.report(s.failure(provider.ErrorInvalidResponse, "response parse failed", err))
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

func (s *webSocketSession) failure(kind provider.ErrorKind, message string, err error) error {
	if code, ok := FailureCode(err); ok && code == s.dialect.SessionExpiredCode {
		return provider.Wrap(provider.ErrorASRSessionExpired, s.name+" session expired", err)
	}
	return provider.Wrap(kind, s.name+" "+message, err)
}

func (s *webSocketSession) noteSessionFailure(err error, counted *bool) {
	if counted == nil || *counted {
		return
	}
	code, ok := FailureCode(err)
	if !ok {
		return
	}
	*counted = true
	asrmetric.Record(s.ctx, s.name, code)
}

func (s *webSocketSession) report(err error) {
	s.readErr = err
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
