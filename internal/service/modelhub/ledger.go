package modelhub

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func (s *Service) shouldRecord(err error) bool {
	// 未触达供应商（本地拒识）不落账本；成功路径 err=nil 必须记录。
	if err == nil {
		return true
	}
	return !provider.IsNotAttempted(err)
}

func (s *Service) baseRecord(ctx context.Context, operation, capability, model, providerName string, startedAt time.Time) callledger.Record {
	return callledger.Record{
		CallID:         uuid.NewString(),
		CallerService:  callledger.CallerFromContext(ctx),
		BusinessScene:  callledger.BusinessSceneFromContext(ctx),
		Operation:      operation,
		Capability:     capability,
		Model:          model,
		Provider:       providerName,
		DeliveryStatus: callledger.DeliveryOK,
		StartedAt:      startedAt,
	}
}

// stampCallTiming 固定模型调用耗时：provider 调用前开始、返回时结束；不含后续 Send/落库。
func stampCallTiming(rec *callledger.Record, started, finished time.Time) {
	if rec == nil {
		return
	}
	rec.StartedAt = started
	f := finished
	rec.FinishedAt = &f
	if !started.IsZero() {
		rec.LatencyMS = finished.Sub(started).Milliseconds()
	}
}

func (s *Service) finishRecord(rec *callledger.Record, err error, sendErr error) {
	// 耗时应由 stampCallTiming 在 provider 返回时写入；此处仅兜底未打点路径。
	if rec.FinishedAt == nil {
		finished := time.Now()
		rec.FinishedAt = &finished
		if !rec.StartedAt.IsZero() {
			rec.LatencyMS = finished.Sub(rec.StartedAt).Milliseconds()
		}
	}
	if sendErr != nil {
		// 供应商结果已产生但最终回传失败：保留 usage/输出，单独标记传输失败。
		rec.DeliveryStatus = callledger.DeliveryClientSendFailed
		if err == nil {
			rec.Status = callledger.StatusSucceeded
			return
		}
	}
	if err == nil {
		rec.Status = callledger.StatusSucceeded
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		rec.Status = callledger.StatusCancelled
	} else {
		rec.Status = callledger.StatusFailed
	}
	rec.ErrorCategory, rec.ErrorCode, rec.ErrorReason, rec.ErrorMessage = callledger.ClassifyError(err)
}

func intPtr(v int) *int { return &v }

// applyEventUsage 事件携带 usage 时保留最新累计值；取消/失败不得丢已返回量。
func applyEventUsage(rec *callledger.Record, event *modelhubv2.GenerateEvent) {
	if rec == nil || event == nil || event.GetUsage() == nil {
		return
	}
	usage, detail := callledger.UsageFromProto(event.GetUsage())
	rec.Usage, rec.UsageDetail = usage, detail
}
