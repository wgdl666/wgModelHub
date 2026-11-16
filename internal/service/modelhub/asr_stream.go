package modelhub

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// TranscribeSpeechStream 把一条 gRPC 双向流严格绑定到一个厂家会话。
// 首包先完成统一校验再拨号，避免非法请求消耗供应商连接或写入真实调用账本。
func (s *Service) TranscribeSpeechStream(stream modelhubv2.ModelHubService_TranscribeSpeechStreamServer) error {
	ctx, span := telemetry.StartSpan(stream.Context(), "modelhub.TranscribeSpeechStream")
	defer span.End()

	first, err := stream.Recv()
	if err != nil {
		return asrStatus(ctx, provider.NotAttempted(provider.ErrorInvalidArgument, "first ASR message must be start"))
	}
	start := first.GetStart()
	if start == nil {
		return asrStatus(ctx, provider.NotAttempted(provider.ErrorInvalidArgument, "first ASR message must be start"))
	}
	if err := validateTranscribeSpeechStart(start); err != nil {
		return asrStatus(ctx, err)
	}
	binding, err := s.resolve(start.GetModel(), config.CapabilityASR)
	if err != nil {
		return asrStatus(ctx, err)
	}
	if binding.set.ASR == nil {
		return asrStatus(ctx, provider.Errorf(provider.ErrorConfiguration, "model %s does not support ASR", start.GetModel()))
	}

	callStarted := time.Now()
	rec := s.baseRecord(ctx, start.GetBusinessMetadata(), callledger.OperationTranscribeSpeechStream, callledger.CapabilityASR, binding.model, binding.provider, callStarted)
	rec.InputPayload = callledger.BuildASRInput(start)
	var providerErr error
	tracked := s.track(ctx, &rec, nil)
	defer s.endTrack(tracked, &rec, &providerErr, nil)
	defer func() {
		// 流中任一 SendAudio/Recv/Send 提前返回也必须形成账本终态，不能留下只有请求没有状态的半行。
		if rec.FinishedAt == nil {
			stampCallTiming(&rec, callStarted, time.Now())
			s.finishRecord(&rec, providerErr, nil)
		}
	}()

	var sendMu sync.Mutex
	var transcriptCount, finalCount atomic.Int64
	asrSession, err := binding.set.ASR.OpenASR(ctx, binding.model, start, func(transcript *modelhubv2.TranscribeSpeechTranscript) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		if transcript.GetIsFinal() {
			finalCount.Add(1)
		}
		transcriptCount.Add(1)
		return stream.Send(&modelhubv2.TranscribeSpeechServerMessage{
			Item: &modelhubv2.TranscribeSpeechServerMessage_Transcript{Transcript: transcript},
		})
	})
	if err != nil {
		providerErr = err
		stampCallTiming(&rec, callStarted, time.Now())
		s.finishRecord(&rec, err, nil)
		return asrStatus(ctx, err)
	}
	defer asrSession.Stop()
	if err := stream.Send(&modelhubv2.TranscribeSpeechServerMessage{
		Item: &modelhubv2.TranscribeSpeechServerMessage_Session{
			Session: &modelhubv2.TranscribeSpeechSession{SessionId: asrSession.ID()},
		},
	}); err != nil {
		providerErr = err
		return err
	}

	recv := make(chan asrReceive, 1)
	go receiveASRMessages(stream, recv)
	errs := asrSession.Errors()
	for {
		select {
		case <-ctx.Done():
			providerErr = ctx.Err()
			rec.OutputPayload = callledger.BuildASROutput(int(transcriptCount.Load()), int(finalCount.Load()))
			stampCallTiming(&rec, callStarted, time.Now())
			s.finishRecord(&rec, ctx.Err(), nil)
			return asrStatus(ctx, ctx.Err())
		case sessionErr, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if sessionErr != nil {
				providerErr = sessionErr
				rec.OutputPayload = callledger.BuildASROutput(int(transcriptCount.Load()), int(finalCount.Load()))
				stampCallTiming(&rec, callStarted, time.Now())
				s.finishRecord(&rec, sessionErr, nil)
				return asrStatus(ctx, sessionErr)
			}
		case incoming := <-recv:
			if incoming.err != nil {
				if errors.Is(incoming.err, io.EOF) {
					rec.OutputPayload = callledger.BuildASROutput(int(transcriptCount.Load()), int(finalCount.Load()))
					stampCallTiming(&rec, callStarted, time.Now())
					s.finishRecord(&rec, nil, nil)
					return nil
				}
				providerErr = incoming.err
				return incoming.err
			}
			switch item := incoming.message.GetItem().(type) {
			case *modelhubv2.TranscribeSpeechClientMessage_Audio:
				if len(item.Audio.GetData()) == 0 {
					providerErr = provider.New(provider.ErrorInvalidArgument, "ASR audio data is empty")
					return asrStatus(ctx, providerErr)
				}
				if err := asrSession.SendAudio(item.Audio.GetData()); err != nil {
					providerErr = err
					return asrStatus(ctx, err)
				}
			case *modelhubv2.TranscribeSpeechClientMessage_Finalize:
				if err := asrSession.Finalize(); err != nil {
					providerErr = err
					return asrStatus(ctx, err)
				}
			case *modelhubv2.TranscribeSpeechClientMessage_Stop:
				err := asrSession.Stop()
				providerErr = err
				rec.OutputPayload = callledger.BuildASROutput(int(transcriptCount.Load()), int(finalCount.Load()))
				stampCallTiming(&rec, callStarted, time.Now())
				s.finishRecord(&rec, err, nil)
				return asrStatus(ctx, err)
			case *modelhubv2.TranscribeSpeechClientMessage_Start:
				providerErr = provider.New(provider.ErrorInvalidArgument, "ASR start may only be sent once")
				return asrStatus(ctx, providerErr)
			default:
				providerErr = provider.New(provider.ErrorInvalidArgument, "ASR message item is required")
				return asrStatus(ctx, providerErr)
			}
		}
	}
}

type asrReceive struct {
	message *modelhubv2.TranscribeSpeechClientMessage
	err     error
}

func receiveASRMessages(stream modelhubv2.ModelHubService_TranscribeSpeechStreamServer, out chan<- asrReceive) {
	for {
		message, err := stream.Recv()
		out <- asrReceive{message: message, err: err}
		if err != nil {
			return
		}
	}
}

func validateTranscribeSpeechStart(start *modelhubv2.TranscribeSpeechStart) error {
	if start == nil || strings.TrimSpace(start.GetModel()) == "" {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "ASR model is required")
	}
	if start.GetEncoding() != modelhubv2.AudioEncoding_AUDIO_ENCODING_PCM_S16LE {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "ASR encoding must be PCM_S16LE")
	}
	if start.GetSampleRateHz() != 16000 {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "ASR sample_rate_hz must be 16000")
	}
	if start.GetChannels() != 1 {
		return provider.NotAttempted(provider.ErrorInvalidArgument, "ASR channels must be 1")
	}
	if silence := start.GetTurnSilence(); silence != nil {
		if silence.GetMinMs() < 0 || silence.GetMaxMs() < 0 {
			return provider.NotAttempted(provider.ErrorInvalidArgument, "ASR turn_silence values must not be negative")
		}
		if silence.GetMinMs() > 0 && silence.GetMaxMs() > 0 && silence.GetMinMs() > silence.GetMaxMs() {
			return provider.NotAttempted(provider.ErrorInvalidArgument, "ASR turn_silence min_ms must not exceed max_ms")
		}
	}
	return nil
}

func asrStatus(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	statusErr := provider.ToStatus(err)
	telemetry.RecordError(ctx, statusErr)
	return statusErr
}
