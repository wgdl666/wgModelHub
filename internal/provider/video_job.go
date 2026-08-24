package provider

import (
	"context"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

// VideoJobState 是供应商侧单次查询结果；对外 RPC 状态由 service 结合本地表再映射。
type VideoJobState int

const (
	VideoJobRunning VideoJobState = iota
	VideoJobSucceeded
	VideoJobFailed
)

// VideoJob 是 GetVideo 的归一化快照；Err 仅在 Failed 时有意义。
type VideoJob struct {
	State       VideoJobState
	PollAfterMs int32
	Err         error
}

// RunVideoJob 供迁移期 Generate(video) 复用：Submit 后按 Get 的 poll_after_ms 阻塞轮询，成功再 ReadResult。
// 这是同步 RPC 内的前台等待，不是后台 worker。
func RunVideoJob(ctx context.Context, video VideoProvider, model string, request *modelhubv2.GenerateRequest, emit EmitEvent) error {
	if video == nil {
		return New(ErrorConfiguration, "video provider is required")
	}
	providerTaskID, err := video.SubmitVideo(ctx, model, request)
	if err != nil {
		return err
	}
	for {
		job, err := video.GetVideo(ctx, model, providerTaskID)
		if err != nil {
			return err
		}
		switch job.State {
		case VideoJobSucceeded:
			return video.ReadVideoResult(ctx, model, providerTaskID, emit)
		case VideoJobFailed:
			if job.Err != nil {
				return job.Err
			}
			return New(ErrorUnavailable, "video job failed")
		default:
			wait := time.Duration(job.PollAfterMs) * time.Millisecond
			if wait <= 0 {
				wait = time.Second
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}
