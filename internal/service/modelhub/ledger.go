package modelhub

import (
	"context"
	"time"

	"github.com/google/uuid"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// announceCall 在账本记录打开后立刻把 call_id 作为响应头发出，不等第一条事件。
// 竞速里落败的一路会被调用方取消，晚发就拿不到；没开账本（tracked 为 nil）时不发，避免给出查不到的编号。
// 发送失败只说明调用方已经断开，不影响本次调用本身。
func announceCall(stream grpc.ServerStream, tracked *callledger.Call, callID string) {
	if stream == nil || tracked == nil || callID == "" {
		return
	}
	_ = stream.SendHeader(metadata.Pairs(protocol.CallIDMetadataKey, callID))
}

func (s *Service) shouldRecord(err error) bool {
	// 未触达供应商（本地拒识）不落账本；成功路径 err=nil 必须记录。
	if err == nil {
		return true
	}
	return !provider.IsNotAttempted(err)
}

func (s *Service) baseRecord(ctx context.Context, business *modelhubv2.BusinessMetadata, operation, capability, model, providerName string, startedAt time.Time) callledger.Record {
	return callledger.Record{
		CallID:           uuid.NewString(),
		CallerService:    callledger.CallerFromContext(ctx),
		BusinessLine:     business.GetBusinessLine(),
		BusinessScene:    business.GetBusinessScene(),
		BusinessSubscene: business.GetBusinessSubscene(),
		TraceID:          callledger.TraceIDFromContext(ctx),
		Operation:        operation,
		Capability:       capability,
		Model:            model,
		Provider:         providerName,
		DeliveryStatus:   callledger.DeliveryOK,
		StartedAt:        startedAt,
	}
}

// stampCallTiming 固定模型调用耗时：provider 调用前开始、返回时结束；不含后续 Send/落库。
// started_at 在 Ent 中 Immutable，只能在 track 插入前写入；此处主要写 FinishedAt/LatencyMS。
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
	// 先归因再定 status：VTON 竞速淘汰常以 gRPC codes.Canceled 到达，
	// 只认 errors.Is(context.Canceled) 会落成 failed，业务日报成功率被拉成 0。
	rec.ErrorCategory, rec.ErrorCode, rec.ErrorReason, rec.ErrorMessage = callledger.ClassifyError(err)
	if rec.ErrorCategory == callledger.ErrorCategoryCancelled {
		rec.Status = callledger.StatusCancelled
	} else {
		rec.Status = callledger.StatusFailed
	}
}

func intPtr(v int) *int { return &v }

// ledgerCallID 找回这条异步视频在账本里的 call_id。
// 对象存储路径必须用它，评测页才允许按 call_id 把视频播出来。
func (s *Service) ledgerCallID(ctx context.Context, generationTaskID string) string {
	if s == nil || generationTaskID == "" {
		return ""
	}
	finder, ok := s.ledger.(callledger.GenerationCallFinder)
	if !ok {
		return ""
	}
	id, err := finder.CallIDByGenerationTask(ctx, generationTaskID)
	if err != nil {
		return ""
	}
	return id
}

// needsVideoArchive：成功任务再查时，只有账本还没有可播放 object_key 才补写归档。
// 已有地址则保持「只回字节、不改账本」，避免覆盖或重复上传。
func (s *Service) needsVideoArchive(ctx context.Context, generationTaskID string) bool {
	if s == nil || generationTaskID == "" {
		return false
	}
	lookup, ok := s.ledger.(callledger.GenerationOutputLookup)
	if !ok {
		return false
	}
	payload, err := lookup.OutputByGenerationTask(ctx, generationTaskID)
	if err != nil {
		// 没有账本行时无法定位 call_id，补归档也会被跳过。
		return false
	}
	return callledger.FirstMediaKey(payload) == ""
}

// applyEventUsage 事件携带 usage 时保留最新累计值；取消/失败不得丢已返回量。
func applyEventUsage(rec *callledger.Record, event *modelhubv2.GenerateEvent) {
	if rec == nil || event == nil || event.GetUsage() == nil {
		return
	}
	usage, detail := callledger.UsageFromProto(event.GetUsage())
	rec.Usage, rec.UsageDetail = usage, detail
}
