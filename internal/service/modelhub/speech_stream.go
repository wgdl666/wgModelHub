package modelhub

import (
	"strings"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"google.golang.org/grpc/metadata"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

// SynthesizeSpeechStream 的成功终态是正常 EOF；首包后失败仍以 gRPC error 结束，不重放已发声音。
func (s *Service) SynthesizeSpeechStream(request *modelhubv2.SynthesizeSpeechRequest, stream modelhubv2.ModelHubService_SynthesizeSpeechStreamServer) error {
	ctx, span := telemetry.StartSpan(stream.Context(), "modelhub.SynthesizeSpeechStream")
	defer span.End()
	if err := validateSynthesizeSpeechRequest(request); err != nil {
		return provider.ToStatus(err)
	}
	binding, err := s.resolve(request.GetModel(), config.CapabilitySpeech)
	if err != nil {
		return provider.ToStatus(err)
	}
	speech, ok := binding.set.Speech.(provider.StreamingSpeechProvider)
	if !ok {
		return provider.ToStatus(provider.Errorf(provider.ErrorConfiguration, "model %s does not support streaming speech", request.GetModel()))
	}
	mime := strings.TrimSpace(speech.SpeechMIME())
	if mime == "" {
		return provider.ToStatus(provider.New(provider.ErrorConfiguration, "streaming speech mime is required"))
	}
	// 插入前写入真实 StartedAt；流式首包耗时仍用下方 provider 边界的 started。
	started := time.Now()
	rec := s.baseRecord(ctx, request.GetBusinessMetadata(), callledger.OperationSynthesizeSpeechStream, callledger.CapabilitySpeech, binding.model, binding.provider, started)
	rec.InputPayload = callledger.BuildSpeechInput(request)
	var providerErr error
	tracked := s.track(ctx, &rec, nil)
	defer s.endTrack(tracked, &rec, &providerErr, nil)
	started = time.Now()
	total, chunks := 0, 0
	var firstChunkMs int64
	var sendErr error
	// 回调到达与下游 Send 分别计时，避免把下游背压误判为供应商合成停顿。
	md, _ := metadata.FromIncomingContext(ctx)
	requestID := ""
	if values := md.Get("x-wg-tts-request-id"); len(values) > 0 && len(values[0]) <= 128 {
		requestID = values[0]
	}
	lastArrival, lastSendEnd := time.Time{}, started
	var maxGap, maxSendMs int64
	defer func() {
		logs.Default().InfoContext(ctx, "tts_provider_summary", "tts_request_id", requestID, "model", binding.model, "provider", binding.provider, "chunks", chunks, "bytes", total, "max_arrival_gap_ms", maxGap, "max_send_ms", maxSendMs, "complete", providerErr == nil, "elapsed_ms", time.Since(started).Milliseconds())
	}()
	err = speech.SynthesizeSpeechStream(ctx, binding.model, request, func(data []byte) error {
		arrived := time.Now()
		gap := int64(0)
		if !lastArrival.IsZero() {
			gap = arrived.Sub(lastArrival).Milliseconds()
		}
		if gap > maxGap {
			maxGap = gap
		}
		if chunks == 0 || gap >= 400 {
			logs.Default().InfoContext(ctx, "tts_provider_chunk", "tts_request_id", requestID, "model", binding.model, "provider", binding.provider, "chunk", chunks+1, "bytes", len(data), "total_bytes", total+len(data), "arrival_gap_ms", gap, "upstream_wait_ms", arrived.Sub(lastSendEnd).Milliseconds(), "received_at_ms", arrived.UnixMilli())
		}
		lastArrival = arrived
		// provider 边界逐块校验总量，避免将空音频或无限流判成成功；不缓存媒体正文。
		if len(data) == 0 {
			return provider.New(provider.ErrorInvalidResponse, "empty speech chunk")
		}
		if total+len(data) > protocol.MaxMediaBytes {
			return provider.New(provider.ErrorInvalidResponse, "speech audio exceeds byte limit")
		}
		if chunks == 0 {
			firstChunkMs = time.Since(started).Milliseconds()
		}
		sendStarted := time.Now()
		sendErr = stream.Send(&modelhubv2.SynthesizeSpeechResponse{Audio: &modelhubv2.Media{
			MimeType: mime, Source: &modelhubv2.Media_Data{Data: data},
		}})
		lastSendEnd = time.Now()
		sendMs := lastSendEnd.Sub(sendStarted).Milliseconds()
		if sendMs > maxSendMs {
			maxSendMs = sendMs
		}
		if sendMs >= 100 || sendErr != nil {
			logs.Default().InfoContext(ctx, "tts_provider_send", "tts_request_id", requestID, "chunk", chunks+1, "send_ms", sendMs, "failed", sendErr != nil)
		}
		if sendErr != nil {
			return sendErr
		}
		total += len(data)
		chunks++
		return nil
	})
	if err == nil && total == 0 {
		err = provider.New(provider.ErrorInvalidResponse, "speech provider returned empty audio")
	}
	stampCallTiming(&rec, started, time.Now())
	providerErr = err
	rec.OutputPayload = map[string]any{"mime_type": mime, "audio_bytes": total, "chunks": chunks, "first_chunk_ms": firstChunkMs}
	if err == nil || s.shouldRecord(err) {
		s.finishRecord(&rec, err, sendErr)
	}
	if sendErr != nil {
		telemetry.RecordError(ctx, sendErr)
		return sendErr
	}
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	return nil
}
