package tencent

import (
	"context"
	"strings"
	"sync"

	"github.com/google/uuid"
	tasr "github.com/tencentcloud/tencentcloud-speech-sdk-go/asr"
	"github.com/tencentcloud/tencentcloud-speech-sdk-go/common"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	asrcommon "github.com/wgdl666/wgModelHub/internal/infra/asr"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Config struct {
	AppID, SecretID, SecretKey, ProxyURL string
}
type Provider struct{ cfg Config }

func New(cfg Config) (provider.ASRProvider, error) {
	if cfg.AppID == "" || cfg.SecretID == "" || cfg.SecretKey == "" {
		return nil, provider.New(provider.ErrorConfiguration, "tencent asr app_id, secret_id and secret_key are required")
	}
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) OpenASR(ctx context.Context, model string, start *modelhubv2.TranscribeSpeechStart, emit provider.ASREmit) (provider.ASRSession, error) {
	// 腾讯 SDK 把 engine_model_type 放在建连参数中，因此真实 model ID 必须原样传入，不能再藏配置 alias。
	asrcommon.LogIgnoredFeatures(ctx, asrcommon.Dialect{Name: "tencent"}, start.GetFeatures())
	s := &session{id: uuid.NewString(), emit: emit, errs: make(chan error, 1)}
	listener := &recognitionListener{session: s}
	recognizer := tasr.NewSpeechRecognizer(p.cfg.AppID, common.NewCredential(p.cfg.SecretID, p.cfg.SecretKey), model, listener)
	if p.cfg.ProxyURL != "" {
		recognizer.ProxyURL = p.cfg.ProxyURL
	}
	recognizer.VoiceFormat = tasr.AudioFormatPCM
	if err := recognizer.Start(); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "start tencent recognizer", err)
	}
	s.recognizer = recognizer
	go func() {
		<-ctx.Done()
		_ = s.Stop()
	}()
	return s, nil
}

type session struct {
	id         string
	emit       provider.ASREmit
	errs       chan error
	recognizer *tasr.SpeechRecognizer
	once       sync.Once
	mu         sync.Mutex
	sentence   int
}

func (s *session) ID() string           { return s.id }
func (s *session) Errors() <-chan error { return s.errs }
func (s *session) SendAudio(data []byte) error {
	if err := s.recognizer.Write(data); err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "tencent asr send audio", err)
	}
	return nil
}
func (s *session) Finalize() error { return nil }
func (s *session) Stop() error {
	s.once.Do(func() { s.recognizer.Stop() })
	return nil
}

type recognitionListener struct{ session *session }

func (l *recognitionListener) OnRecognitionStart(*tasr.SpeechRecognitionResponse) {}
func (l *recognitionListener) OnSentenceBegin(*tasr.SpeechRecognitionResponse) {
	l.session.mu.Lock()
	l.session.sentence++
	l.session.mu.Unlock()
}
func (l *recognitionListener) OnRecognitionResultChange(response *tasr.SpeechRecognitionResponse) {
	l.emit(response, false)
}
func (l *recognitionListener) OnSentenceEnd(response *tasr.SpeechRecognitionResponse) {
	l.emit(response, true)
}
func (l *recognitionListener) OnRecognitionComplete(*tasr.SpeechRecognitionResponse) {}
func (l *recognitionListener) OnFail(_ *tasr.SpeechRecognitionResponse, err error) {
	select {
	case l.session.errs <- provider.Wrap(provider.ErrorUnavailable, "tencent recognition failed", err):
	default:
	}
}
func (l *recognitionListener) emit(response *tasr.SpeechRecognitionResponse, final bool) {
	text := strings.TrimSpace(response.Result.VoiceTextStr)
	if text == "" {
		return
	}
	if err := l.session.emit(&modelhubv2.TranscribeSpeechTranscript{Text: text, IsFinal: final}); err != nil {
		select {
		case l.session.errs <- err:
		default:
		}
	}
}
