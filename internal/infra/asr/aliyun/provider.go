package aliyun

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	nls "github.com/aliyun/alibabacloud-nls-go-sdk"
	"github.com/google/uuid"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	asrcommon "github.com/wgdl666/wgModelHub/internal/infra/asr"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Config struct {
	AKID, AKKey, AppKey, Token, URL string
}
type Provider struct{ cfg Config }

func New(cfg Config) (provider.ASRProvider, error) {
	if strings.TrimSpace(cfg.AppKey) == "" {
		return nil, provider.New(provider.ErrorConfiguration, "aliyun nls app_key is required")
	}
	if cfg.Token == "" && (cfg.AKID == "" || cfg.AKKey == "") {
		return nil, provider.New(provider.ErrorConfiguration, "aliyun nls token or AK pair is required")
	}
	return &Provider{cfg: cfg}, nil
}

func (p *Provider) OpenASR(ctx context.Context, _ string, start *modelhubv2.TranscribeSpeechStart, emit provider.ASREmit) (provider.ASRSession, error) {
	asrcommon.LogIgnoredFeatures(ctx, asrcommon.Dialect{Name: "aliyun-nls"}, start.GetFeatures())
	endpoint := p.cfg.URL
	if endpoint == "" {
		endpoint = nls.DEFAULT_URL
	}
	var conn *nls.ConnectionConfig
	var err error
	if p.cfg.Token != "" {
		conn = nls.NewConnectionConfigWithToken(endpoint, p.cfg.AppKey, p.cfg.Token)
	} else {
		conn, err = nls.NewConnectionConfigWithAKInfoDefault(endpoint, p.cfg.AppKey, p.cfg.AKID, p.cfg.AKKey)
	}
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "aliyun nls connection config", err)
	}
	logger := nls.NewNlsLogger(os.Stderr, "[ModelHub/AliyunASR] ", log.LstdFlags)
	logger.SetLogSil(true)
	s := &session{id: uuid.NewString(), emit: emit, errs: make(chan error, 1)}
	fail := func(text string, _ any) {
		s.report(provider.New(provider.ErrorUnavailable, "aliyun nls task failed: "+text))
	}
	started := func(string, any) {}
	begin := func(string, any) {}
	result := func(text string, final bool) {
		var response struct {
			Payload struct {
				Result     string  `json:"result"`
				Confidence float64 `json:"confidence"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(text), &response) != nil {
			return
		}
		if transcript := strings.TrimSpace(response.Payload.Result); transcript != "" {
			if err := emit(&modelhubv2.TranscribeSpeechTranscript{Text: transcript, IsFinal: final, Confidence: response.Payload.Confidence}); err != nil {
				s.report(err)
			}
		}
	}
	st, err := nls.NewSpeechTranscription(conn, logger, fail, started, begin,
		func(text string, _ any) { result(text, true) },
		func(text string, _ any) { result(text, false) },
		func(string, any) {}, func(any) {}, logger)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "create aliyun nls transcription", err)
	}
	param := nls.DefaultSpeechTranscriptionParam()
	param.Format = nls.PCM
	ready, err := st.Start(param, nil)
	if err != nil {
		st.Shutdown()
		return nil, provider.Wrap(provider.ErrorUnavailable, "start aliyun nls transcription", err)
	}
	select {
	case ok := <-ready:
		if !ok {
			st.Shutdown()
			return nil, provider.New(provider.ErrorUnavailable, "aliyun nls ready=false")
		}
	case <-ctx.Done():
		st.Shutdown()
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		st.Shutdown()
		return nil, provider.New(provider.ErrorTimeout, "aliyun nls start timeout")
	}
	s.st = st
	go func() {
		<-ctx.Done()
		_ = s.Stop()
	}()
	// NLS SDK 暂无稳定的请求级内联 keyterms；热词表继续由控制台 AppKey 绑定，不能伪造已下发。
	return s, nil
}

type session struct {
	id   string
	st   *nls.SpeechTranscription
	emit provider.ASREmit
	errs chan error
	once sync.Once
}

func (s *session) ID() string           { return s.id }
func (s *session) Errors() <-chan error { return s.errs }
func (s *session) SendAudio(data []byte) error {
	if err := s.st.SendAudioData(data); err != nil {
		return provider.Wrap(provider.ErrorUnavailable, "aliyun nls send audio", err)
	}
	return nil
}
func (s *session) Finalize() error { return nil }
func (s *session) Stop() error {
	var ret error
	s.once.Do(func() {
		if ready, err := s.st.Stop(); err != nil {
			ret = err
		} else {
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
			}
		}
		s.st.Shutdown()
	})
	return ret
}
func (s *session) report(err error) {
	select {
	case s.errs <- err:
	default:
	}
}
