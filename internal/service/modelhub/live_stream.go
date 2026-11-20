package modelhub

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	v2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// LiveStream 只转发模型事件；不执行工具，不把音频生成视为业务或客户端播放完成。
func (s *Service) LiveStream(stream v2.ModelHubService_LiveStreamServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	ctx, span := telemetry.StartSpan(ctx, "modelhub.LiveStream")
	defer span.End()
	first, err := stream.Recv()
	if err != nil || first.GetStart() == nil {
		return provider.ToStatus(provider.NotAttempted(provider.ErrorInvalidArgument, "first Live message must be start"))
	}
	start := first.GetStart()
	if err := validateInput(start.GetInput()); err != nil {
		return provider.ToStatus(err)
	}
	binding, err := s.resolve(start.GetModel(), config.CapabilityLive)
	if err != nil {
		return provider.ToStatus(err)
	}
	if binding.set.Live == nil {
		return provider.ToStatus(provider.NotAttempted(provider.ErrorConfiguration, "provider does not implement Live"))
	}
	began := time.Now()
	rec := s.baseRecord(ctx, start.BusinessMetadata, "live_stream", "live", binding.model, binding.provider, began)
	// 实时录音和提示词留在业务归档；模型账本只保存归因、耗时和终态。
	var callErr error
	tracked := s.track(ctx, &rec, nil)
	defer func() {
		stampCallTiming(&rec, began, time.Now())
		s.finishRecord(&rec, callErr, nil)
		s.endTrack(tracked, &rec, &callErr, nil)
	}()
	session, err := binding.set.Live.OpenLive(ctx, binding.model, start)
	if err != nil {
		callErr = err
		return provider.ToStatus(err)
	}
	defer session.Close()
	// Recv 的阻塞由 RPC 返回时的 context 取消解除；两方向只有一个 writer。
	errs := make(chan error, 2)
	go func() {
		for {
			m, e := stream.Recv()
			if e != nil {
				errs <- e
				return
			}
			if input := m.GetContext(); input != nil {
				if e = validateInput(input); e != nil {
					errs <- e
					return
				}
			}
			if e = session.Send(m); e != nil {
				errs <- e
				return
			}
		}
	}()
	go func() {
		for {
			m, e := session.Receive()
			if e != nil {
				errs <- e
				return
			}
			if e = stream.Send(m); e != nil {
				errs <- e
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		callErr = ctx.Err()
	case callErr = <-errs:
		if errors.Is(callErr, io.EOF) {
			callErr = nil
		}
	}
	return provider.ToStatus(callErr)
}
