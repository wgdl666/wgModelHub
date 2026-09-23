package callledger

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/modelcall"
)

// 落库短超时：best effort，失败可观测且不得重调模型或改写已成功调用结果。
const persistTimeout = 2 * time.Second

// Store 是调用账本写入面；实现必须短超时且不因失败阻塞调用方结果。
type Store interface {
	Record(ctx context.Context, rec Record) error
	UpsertByGenerationTask(ctx context.Context, rec Record) error
}

// Record 是一条逻辑模型生成调用的落库事实。
type Record struct {
	CallID           string
	GenerationTaskID string
	CallerService    string
	BusinessScene    string
	Operation        string
	Capability       string
	Model            string
	Provider         string
	Status           string
	DeliveryStatus   string
	ErrorCategory    string
	ErrorCode        string
	ErrorReason      string
	ErrorMessage     string
	Usage            *Usage
	UsageDetail      map[string]any
	ImageCount       *int
	ImageSize        string
	ImageAspectRatio string
	VideoCount       *int
	VideoResolution  string
	VideoDurationSec *int
	VideoAspectRatio string
	LatencyMS        int64
	StartedAt        time.Time
	FinishedAt       *time.Time
	InputPayload     map[string]any
	OutputPayload    map[string]any
}

// Usage 仅在供应商返回 usage 对象时非 nil；字段值可为 0。
type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	TotalTokens     int64
	CachedTokens    int64
	ReasoningTokens int64
}

const (
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	DeliveryOK               = "ok"
	DeliveryClientSendFailed = "client_send_failed"

	ErrorCategoryCallerRequest = "caller_request"
	ErrorCategoryProvider      = "provider"
	ErrorCategoryModelHub      = "modelhub"
	ErrorCategoryCancelled     = "cancelled"
	ErrorCategoryUnknown       = "unknown"

	OperationGenerateText     = "generate_text"
	OperationGenerateImage    = "generate_image"
	OperationGenerateVideo    = "generate_video"
	OperationSubmitGeneration = "submit_generation"
	OperationSynthesizeSpeech = "synthesize_speech"

	CapabilityText   = "text"
	CapabilityImage  = "image"
	CapabilityVideo  = "video"
	CapabilitySpeech = "speech"

	UnknownLabel = "unknown"
)

// upsertMaxAttempts：唯一键冲突或乐观锁版本冲突时有界重读重试。
const upsertMaxAttempts = 8

// Postgres 经 Ent 写入 modelhub.model_call；不做启动 DDL。
type Postgres struct {
	client *ent.Client
	// afterReadForTest 仅单测注入：两个写者先读到同一行后再写，验证乐观锁。
	afterReadForTest func(existing *ent.ModelCall)
}

func NewPostgres(client *ent.Client) *Postgres {
	if client == nil {
		return nil
	}
	return &Postgres{client: client}
}

// Memory 供单测断言落库内容；并发安全不在此保证以外的生产路径使用。
type Memory struct {
	Records []Record
}

func (m *Memory) Record(_ context.Context, rec Record) error {
	if rec.CallID == "" {
		rec.CallID = uuid.NewString()
	}
	m.Records = append(m.Records, rec)
	return nil
}

func (m *Memory) UpsertByGenerationTask(_ context.Context, rec Record) error {
	if rec.GenerationTaskID == "" {
		return errors.New("generation_task_id required")
	}
	for i := range m.Records {
		if m.Records[i].GenerationTaskID == rec.GenerationTaskID {
			m.Records[i] = mergeVideoRecord(m.Records[i], rec)
			return nil
		}
	}
	if rec.CallID == "" {
		rec.CallID = uuid.NewString()
	}
	m.Records = append(m.Records, rec)
	return nil
}

func (p *Postgres) Record(ctx context.Context, rec Record) error {
	if p == nil || p.client == nil {
		return nil
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if rec.CallID == "" {
		rec.CallID = uuid.NewString()
	}
	builder := p.client.ModelCall.Create().
		SetID(rec.CallID).
		SetCallerService(normalizeLabel(rec.CallerService)).
		SetBusinessScene(normalizeLabel(rec.BusinessScene)).
		SetOperation(rec.Operation).
		SetCapability(rec.Capability).
		SetModel(rec.Model).
		SetProvider(rec.Provider).
		SetStatus(rec.Status).
		SetDeliveryStatus(defaultDelivery(rec.DeliveryStatus)).
		SetErrorCategory(rec.ErrorCategory).
		SetErrorCode(rec.ErrorCode).
		SetErrorReason(rec.ErrorReason).
		SetErrorMessage(sanitizeErrorMessage(rec.ErrorMessage)).
		SetImageSize(rec.ImageSize).
		SetImageAspectRatio(rec.ImageAspectRatio).
		SetVideoResolution(rec.VideoResolution).
		SetVideoAspectRatio(rec.VideoAspectRatio).
		SetLatencyMs(rec.LatencyMS).
		SetStartedAt(rec.StartedAt).
		SetInputPayload(nonNilMap(rec.InputPayload)).
		SetOutputPayload(nonNilMap(rec.OutputPayload)).
		SetUsageDetail(nonNilMap(rec.UsageDetail))
	if rec.GenerationTaskID != "" {
		builder.SetGenerationTaskID(rec.GenerationTaskID)
	}
	applyOptionalUsage(builder, rec.Usage)
	if rec.ImageCount != nil {
		builder.SetImageCount(*rec.ImageCount)
	}
	if rec.VideoCount != nil {
		builder.SetVideoCount(*rec.VideoCount)
	}
	if rec.VideoDurationSec != nil {
		builder.SetVideoDurationSeconds(*rec.VideoDurationSec)
	}
	if rec.FinishedAt != nil {
		builder.SetFinishedAt(*rec.FinishedAt)
	}
	_, err := builder.Save(persistCtx)
	return err
}

// UpsertByGenerationTask：generation_task_id 唯一；用读到的 status 做 CAS 乐观锁，冲突则重读重试。
func (p *Postgres) UpsertByGenerationTask(ctx context.Context, rec Record) error {
	if p == nil || p.client == nil {
		return nil
	}
	if rec.GenerationTaskID == "" {
		return errors.New("generation_task_id required")
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()

	for attempt := 0; attempt < upsertMaxAttempts; attempt++ {
		existing, err := p.client.ModelCall.Query().
			Where(modelcall.GenerationTaskIDEQ(rec.GenerationTaskID)).
			Only(persistCtx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return err
			}
			if rec.CallID == "" {
				rec.CallID = uuid.NewString()
			}
			if err := p.Record(persistCtx, rec); err != nil {
				if ent.IsConstraintError(err) {
					continue
				}
				return err
			}
			return nil
		}
		if p.afterReadForTest != nil {
			p.afterReadForTest(existing)
		}
		merged := mergeVideoRecord(fromEnt(existing), rec)
		err = p.updateMergedOptimistic(persistCtx, existing, merged)
		if err != nil {
			// CAS 未命中（他写者已改 status）→ 重读合并，避免仅靠内存 existing.Status 误判。
			if ent.IsNotFound(err) {
				continue
			}
			return err
		}
		return nil
	}
	return errors.New("upsert generation_task_id conflict retry exhausted")
}

func (p *Postgres) updateMergedOptimistic(ctx context.Context, existing *ent.ModelCall, merged Record) error {
	// 以读到的 status 做 DB 原子 CAS：他写者已推进终态则 Save 返回 NotFound，外层重读合并。
	// 不用 updated_at：SQLite 驱动时间精度会导致条件永不命中。
	upd := p.client.ModelCall.UpdateOneID(existing.ID).
		Where(modelcall.StatusEQ(existing.Status)).
		SetDeliveryStatus(defaultDelivery(merged.DeliveryStatus)).
		SetErrorCategory(merged.ErrorCategory).
		SetErrorCode(merged.ErrorCode).
		SetErrorReason(merged.ErrorReason).
		SetErrorMessage(sanitizeErrorMessage(merged.ErrorMessage)).
		SetOutputPayload(nonNilMap(merged.OutputPayload)).
		SetUsageDetail(nonNilMap(merged.UsageDetail))
	applyOptionalUsageUpdate(upd, merged.Usage)

	// 模型终态与耗时只在首次进入终态时写入；后续 Get/下载/发送失败不得延长或回退。
	if !isTerminalStatus(existing.Status) {
		upd.SetStatus(merged.Status)
		upd.SetLatencyMs(merged.LatencyMS)
		if merged.FinishedAt != nil {
			upd.SetFinishedAt(*merged.FinishedAt)
		}
	}

	// 输入/场景/规格保留 Submit 来源，禁止后续查询 metadata 覆盖。
	if len(existing.InputPayload) == 0 && len(merged.InputPayload) > 0 {
		upd.SetInputPayload(nonNilMap(merged.InputPayload))
	}
	if existing.CallerService == UnknownLabel && merged.CallerService != "" && merged.CallerService != UnknownLabel {
		upd.SetCallerService(normalizeLabel(merged.CallerService))
	}
	if existing.BusinessScene == UnknownLabel && merged.BusinessScene != "" && merged.BusinessScene != UnknownLabel {
		upd.SetBusinessScene(normalizeLabel(merged.BusinessScene))
	}
	if existing.ImageSize == "" && merged.ImageSize != "" {
		upd.SetImageSize(merged.ImageSize)
	}
	if existing.ImageAspectRatio == "" && merged.ImageAspectRatio != "" {
		upd.SetImageAspectRatio(merged.ImageAspectRatio)
	}
	if existing.VideoResolution == "" && merged.VideoResolution != "" {
		upd.SetVideoResolution(merged.VideoResolution)
	}
	if existing.VideoAspectRatio == "" && merged.VideoAspectRatio != "" {
		upd.SetVideoAspectRatio(merged.VideoAspectRatio)
	}
	if existing.VideoDurationSeconds == nil && merged.VideoDurationSec != nil {
		upd.SetVideoDurationSeconds(*merged.VideoDurationSec)
	}
	if merged.ImageCount != nil {
		upd.SetImageCount(*merged.ImageCount)
	}
	if merged.VideoCount != nil {
		// 不得用 0 覆盖已有成功产物计数。
		if existing.VideoCount == nil || *merged.VideoCount > 0 {
			upd.SetVideoCount(*merged.VideoCount)
		}
	}
	_, err := upd.Save(ctx)
	return err
}

// BestEffort 落库失败只打日志，绝不改变调用结果或触发重试模型。
func BestEffort(ctx context.Context, store Store, rec Record) {
	if store == nil {
		return
	}
	var err error
	if rec.GenerationTaskID != "" {
		err = store.UpsertByGenerationTask(ctx, rec)
	} else {
		err = store.Record(ctx, rec)
	}
	if err != nil {
		slog.Warn("model_call ledger persist failed",
			"operation", rec.Operation,
			"model", rec.Model,
			"provider", rec.Provider,
			"generation_task_id", rec.GenerationTaskID,
			"err", err,
		)
	}
}

func normalizeLabel(v string) string {
	if v == "" {
		return UnknownLabel
	}
	return v
}

// NormalizeOrUnknown 供异步视频从 task.Caller 回填；空串记 unknown。
func NormalizeOrUnknown(v string) string {
	return normalizeLabel(v)
}

func defaultDelivery(v string) string {
	if v == "" {
		return DeliveryOK
	}
	return v
}

func nonNilMap(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func isTerminalStatus(status string) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

func applyOptionalUsage(builder *ent.ModelCallCreate, usage *Usage) {
	if usage == nil {
		return
	}
	builder.
		SetInputTokens(usage.InputTokens).
		SetOutputTokens(usage.OutputTokens).
		SetTotalTokens(usage.TotalTokens).
		SetCachedTokens(usage.CachedTokens).
		SetReasoningTokens(usage.ReasoningTokens)
}

func applyOptionalUsageUpdate(builder *ent.ModelCallUpdateOne, usage *Usage) {
	if usage == nil {
		return
	}
	builder.
		SetInputTokens(usage.InputTokens).
		SetOutputTokens(usage.OutputTokens).
		SetTotalTokens(usage.TotalTokens).
		SetCachedTokens(usage.CachedTokens).
		SetReasoningTokens(usage.ReasoningTokens)
}

func fromEnt(row *ent.ModelCall) Record {
	rec := Record{
		CallID:           row.ID,
		CallerService:    row.CallerService,
		BusinessScene:    row.BusinessScene,
		Operation:        row.Operation,
		Capability:       row.Capability,
		Model:            row.Model,
		Provider:         row.Provider,
		Status:           row.Status,
		DeliveryStatus:   row.DeliveryStatus,
		ErrorCategory:    row.ErrorCategory,
		ErrorCode:        row.ErrorCode,
		ErrorReason:      row.ErrorReason,
		ErrorMessage:     row.ErrorMessage,
		ImageSize:        row.ImageSize,
		ImageAspectRatio: row.ImageAspectRatio,
		VideoResolution:  row.VideoResolution,
		VideoAspectRatio: row.VideoAspectRatio,
		LatencyMS:        row.LatencyMs,
		StartedAt:        row.StartedAt,
		FinishedAt:       row.FinishedAt,
		InputPayload:     row.InputPayload,
		OutputPayload:    row.OutputPayload,
		UsageDetail:      row.UsageDetail,
		ImageCount:       row.ImageCount,
		VideoCount:       row.VideoCount,
		VideoDurationSec: row.VideoDurationSeconds,
	}
	if row.GenerationTaskID != nil {
		rec.GenerationTaskID = *row.GenerationTaskID
	}
	if row.InputTokens != nil || row.OutputTokens != nil || row.TotalTokens != nil ||
		row.CachedTokens != nil || row.ReasoningTokens != nil {
		u := &Usage{}
		if row.InputTokens != nil {
			u.InputTokens = *row.InputTokens
		}
		if row.OutputTokens != nil {
			u.OutputTokens = *row.OutputTokens
		}
		if row.TotalTokens != nil {
			u.TotalTokens = *row.TotalTokens
		}
		if row.CachedTokens != nil {
			u.CachedTokens = *row.CachedTokens
		}
		if row.ReasoningTokens != nil {
			u.ReasoningTokens = *row.ReasoningTokens
		}
		rec.Usage = u
	}
	return rec
}

// mergeVideoRecord：同一 task_id 一条；首次模型终态冻结状态/完成时间/耗时；输入场景保留 Submit。
func mergeVideoRecord(prev, next Record) Record {
	out := prev
	if out.CallID == "" {
		out.CallID = next.CallID
	}
	out.GenerationTaskID = firstNonEmpty(out.GenerationTaskID, next.GenerationTaskID)
	out.CallerService = firstNonEmptyPreferKnown(out.CallerService, next.CallerService)
	// 业务场景以 Submit 写入为准，后续 Get 的 metadata 不得覆盖。
	out.BusinessScene = firstNonEmptyPreferKnown(out.BusinessScene, next.BusinessScene)
	out.Operation = firstNonEmpty(out.Operation, next.Operation)
	out.Capability = firstNonEmpty(out.Capability, next.Capability)
	out.Model = firstNonEmpty(out.Model, next.Model)
	out.Provider = firstNonEmpty(out.Provider, next.Provider)

	prevTerminal := isTerminalStatus(out.Status)
	nextTerminal := isTerminalStatus(next.Status)

	if !prevTerminal {
		if next.Status != "" {
			// 迟到 pending 不得覆盖已有非空状态以外的升级；pending→pending 可保持。
			if next.Status != StatusPending || out.Status == "" || out.Status == StatusPending {
				out.Status = next.Status
			}
		}
		if nextTerminal {
			if next.FinishedAt != nil {
				out.FinishedAt = next.FinishedAt
			}
			if next.LatencyMS > 0 {
				out.LatencyMS = next.LatencyMS
			}
		} else if out.StartedAt.IsZero() && !next.StartedAt.IsZero() {
			out.StartedAt = next.StartedAt
		}
	}
	// 已终态：不改 status / finished_at / latency；仅允许补交付与结果。

	if next.DeliveryStatus != "" {
		out.DeliveryStatus = next.DeliveryStatus
	}
	// 结果读取/发送失败可更新错误摘要，但不把模型成功改成失败。
	if next.ErrorCategory != "" || next.ErrorCode != "" || next.ErrorReason != "" || next.ErrorMessage != "" {
		if !prevTerminal || out.Status != StatusSucceeded || next.DeliveryStatus == DeliveryClientSendFailed {
			out.ErrorCategory = next.ErrorCategory
			out.ErrorCode = next.ErrorCode
			out.ErrorReason = next.ErrorReason
			out.ErrorMessage = next.ErrorMessage
		}
	}
	if next.Usage != nil {
		out.Usage = next.Usage
	}
	if len(next.UsageDetail) > 0 {
		out.UsageDetail = next.UsageDetail
	}
	if next.ImageCount != nil {
		out.ImageCount = next.ImageCount
	}
	if next.VideoCount != nil {
		if out.VideoCount == nil || *next.VideoCount > 0 {
			out.VideoCount = next.VideoCount
		}
	}
	out.VideoResolution = firstNonEmpty(out.VideoResolution, next.VideoResolution)
	if out.VideoDurationSec == nil && next.VideoDurationSec != nil {
		out.VideoDurationSec = next.VideoDurationSec
	}
	out.VideoAspectRatio = firstNonEmpty(out.VideoAspectRatio, next.VideoAspectRatio)
	out.ImageSize = firstNonEmpty(out.ImageSize, next.ImageSize)
	out.ImageAspectRatio = firstNonEmpty(out.ImageAspectRatio, next.ImageAspectRatio)
	if out.StartedAt.IsZero() {
		out.StartedAt = next.StartedAt
	}
	if len(out.InputPayload) == 0 && len(next.InputPayload) > 0 {
		out.InputPayload = next.InputPayload
	}
	// 可补写输出，不得用空 payload 覆盖已有成功结果。
	if len(next.OutputPayload) > 0 {
		if len(out.OutputPayload) == 0 || outputHasSubstance(next.OutputPayload) {
			out.OutputPayload = next.OutputPayload
		}
	}
	return out
}

func outputHasSubstance(payload map[string]any) bool {
	if len(payload) == 0 {
		return false
	}
	if n, ok := payload["video_count"].(int); ok && n > 0 {
		return true
	}
	if n, ok := payload["video_count"].(float64); ok && n > 0 {
		return true
	}
	if n, ok := payload["total_bytes"].(int64); ok && n > 0 {
		return true
	}
	if n, ok := payload["total_bytes"].(float64); ok && n > 0 {
		return true
	}
	if n, ok := payload["chunk_count"].(int); ok && n > 0 {
		return true
	}
	if n, ok := payload["chunk_count"].(float64); ok && n > 0 {
		return true
	}
	return len(payload) > 1
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstNonEmptyPreferKnown(a, b string) string {
	if a != "" && a != UnknownLabel {
		return a
	}
	if b != "" {
		return b
	}
	return a
}
