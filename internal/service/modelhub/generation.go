package modelhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/auth"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/internal/taskstore"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const defaultPollAfterMs int32 = 3000

// persistTimeout：上游已返回 provider_task_id 后，用短超时 detached context 落库，避免客户端刚断开丢 id。
const persistTimeout = 5 * time.Second

// geminiFilesSubmitWindow：Gemini waitFileActive 与 HTTP 客户端上限均为 10 分钟，是含 Files 上传等待的真实 Submit 最长窗口。
const geminiFilesSubmitWindow = 10 * time.Minute

// stalePendingSafetyMargin：给时钟偏差与落库延迟留余量，避免正当 Submit 尚未 MarkRunning 就被误判为僵尸。
const stalePendingSafetyMargin = 2 * time.Minute

// stalePendingWithoutProviderTaskID：无 provider_task_id 的 PENDING 超过「Submit 最长窗口 + 安全余量」视为库中断僵尸任务。
const stalePendingWithoutProviderTaskID = geminiFilesSubmitWindow + stalePendingSafetyMargin

const errorInfoDomain = "wg.modelhub"

// SubmitGeneration 第一阶段只接受 output.video；先落库再调上游，不确定受理时落 FAILED 且不自动重提。
func (s *Service) SubmitGeneration(ctx context.Context, req *modelhubv2.SubmitGenerationRequest) (*modelhubv2.GenerationTask, error) {
	ctx, span := telemetry.StartSpan(ctx, "modelhub.SubmitGeneration")
	defer span.End()

	if s.tasks == nil {
		err := provider.New(provider.ErrorConfiguration, "generation task store is not configured")
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if req == nil || strings.TrimSpace(req.GetRequestId()) == "" {
		err := provider.NotAttempted(provider.ErrorInvalidArgument, "request_id is required")
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	generateReq := req.GetRequest()
	capability, err := capabilityOf(generateReq)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if capability != config.CapabilityVideo {
		err := provider.NotAttempted(provider.ErrorInvalidArgument, "SubmitGeneration only accepts output.video in this phase")
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if err := validateGenerateRequest(generateReq, capability); err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	bound, err := s.resolve(generateReq.GetModel(), capability)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if bound.set.Video == nil {
		err := provider.Errorf(provider.ErrorConfiguration, "model %s does not support video", generateReq.GetModel())
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	// 落库 provider 必须取 resolve 已选定实例；视频任务跨 Pod 查询只认当时写入名，不受重启后 model_routes 改写。
	providerName := bound.provider

	requestHash, err := hashGenerateRequest(generateReq)
	if err != nil {
		statusErr := provider.ToStatus(provider.WrapNotAttempted(provider.ErrorInvalidArgument, "hash generate request", err))
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}

	pending := taskstore.Task{
		TaskID:      uuid.NewString(),
		Caller:      callerFromContext(ctx),
		RequestID:   strings.TrimSpace(req.GetRequestId()),
		RequestHash: requestHash,
		Model:       bound.model,
		Provider:    providerName,
		State:       taskstore.StatePending,
	}
	stored, created, err := s.tasks.InsertPending(ctx, pending)
	if err != nil {
		if errors.Is(err, taskstore.ErrRequestHashConflict) {
			statusErr := status.Error(codes.AlreadyExists, "request_id already used with a different request body")
			telemetry.RecordError(ctx, statusErr)
			return nil, statusErr
		}
		statusErr := provider.ToStatus(provider.Wrap(provider.ErrorUnavailable, "persist generation task", err))
		telemetry.RecordError(ctx, statusErr)
		return nil, statusErr
	}
	if !created {
		// 相同 (caller, request_id, hash) 直接返回原任务，绝不自动再次 Submit 上游。
		return &modelhubv2.GenerationTask{TaskId: stored.TaskID, State: publicState(stored.State)}, nil
	}

	providerTaskID, submitErr := bound.set.Video.SubmitVideo(ctx, bound.model, generateReq)
	submitFinished := time.Now()
	persistCtx, persistCancel := detachPersistContext(ctx)
	defer persistCancel()

	// 真实 Submit 已发出：按 task_id 写入/更新账本；幂等命中不会走到这里。
	rec := s.baseRecord(ctx, callledger.OperationSubmitGeneration, callledger.CapabilityVideo, bound.model, providerName, stored.CreatedAt)
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now()
	}
	rec.GenerationTaskID = stored.TaskID
	rec.InputPayload = callledger.BuildGenerateInput(generateReq)
	resolution, durationSec, aspect := callledger.RequestVideoSpec(generateReq)
	rec.VideoResolution, rec.VideoDurationSec, rec.VideoAspectRatio = resolution, durationSec, aspect
	rec.Status = callledger.StatusPending

	if submitErr != nil {
		telemetry.RecordError(ctx, submitErr)
		code, message, reason := normalizeProviderError(submitErr)
		if isUncertainSubmit(submitErr) {
			// 超时/取消才标 unknown，禁止自动重提；error_message 仍留厂商/底层原文，供落库后查询。
			code = int32(codes.Unavailable)
			reason = string(provider.ErrorSubmitOutcomeUnknown)
		}
		if markErr := s.tasks.MarkFailed(persistCtx, stored.TaskID, taskstore.StatePending, code, message, reason); markErr != nil {
			statusErr := provider.ToStatus(provider.Wrap(provider.ErrorUnavailable, "mark generation task failed", markErr))
			telemetry.RecordError(ctx, statusErr)
			return nil, statusErr
		}
		if s.shouldRecord(submitErr) || isUncertainSubmit(submitErr) {
			stampCallTiming(&rec, rec.StartedAt, submitFinished)
			s.finishRecord(&rec, submitErr, nil)
			callledger.BestEffort(ctx, s.ledger, rec)
		}
		return &modelhubv2.GenerationTask{
			TaskId: stored.TaskID,
			State:  modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED,
		}, nil
	}
	if err := s.tasks.MarkRunning(persistCtx, stored.TaskID, providerName, providerTaskID); err != nil {
		// 已拿到 provider id 但无法落库：不得返回可轮询 PENDING；尽力标 FAILED，RPC 一律 Unavailable。
		_ = s.tasks.MarkFailed(persistCtx, stored.TaskID, taskstore.StatePending, int32(codes.Unavailable),
			"failed to persist provider task id", string(provider.ErrorSubmitOutcomeUnknown))
		statusErr := provider.ToStatus(provider.Wrap(provider.ErrorUnavailable, "persist provider task id", err))
		telemetry.RecordError(ctx, statusErr)
		s.finishRecord(&rec, statusErr, nil)
		callledger.BestEffort(ctx, s.ledger, rec)
		return nil, statusErr
	}
	// 受理成功：保持 pending，无人 Get 则长期未完成；不启后台轮询。
	callledger.BestEffort(ctx, s.ledger, rec)
	return &modelhubv2.GenerationTask{
		TaskId: stored.TaskID,
		State:  modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_RUNNING,
	}, nil
}

// GetGeneration：PENDING/RUNNING/FAILED 只回一个 status；SUCCEEDED 先 status 再复用 GenerateEvent 分块。
func (s *Service) GetGeneration(req *modelhubv2.GetGenerationRequest, stream modelhubv2.ModelHubService_GetGenerationServer) error {
	ctx := stream.Context()
	ctx, span := telemetry.StartSpan(ctx, "modelhub.GetGeneration")
	defer span.End()

	if s.tasks == nil {
		err := provider.New(provider.ErrorConfiguration, "generation task store is not configured")
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	if req == nil || strings.TrimSpace(req.GetTaskId()) == "" {
		err := provider.NotAttempted(provider.ErrorInvalidArgument, "task_id is required")
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	task, err := s.tasks.GetByTaskID(ctx, strings.TrimSpace(req.GetTaskId()))
	if err != nil {
		if errors.Is(err, taskstore.ErrNotFound) {
			statusErr := status.Error(codes.NotFound, "generation task not found")
			telemetry.RecordError(ctx, statusErr)
			return statusErr
		}
		statusErr := provider.ToStatus(provider.Wrap(provider.ErrorUnavailable, "load generation task", err))
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	// 公网已鉴权主体只能读取自己 caller 下的任务，避免仅凭 task_id 泄露跨主体状态。
	if publicCaller, ok := auth.PublicCaller(ctx); ok && task.Caller != publicCaller {
		statusErr := status.Error(codes.NotFound, "generation task not found")
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}

	switch task.State {
	case taskstore.StatePending:
		return s.handlePendingTask(ctx, task, stream)
	case taskstore.StateFailed:
		return stream.Send(statusEvent(modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED, 0, taskRPCStatus(task)))
	case taskstore.StateSucceeded:
		if err := stream.Send(statusEvent(modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_SUCCEEDED, 0, nil)); err != nil {
			return err
		}
		return s.streamVideoResult(ctx, task, stream)
	case taskstore.StateRunning:
		return s.pollRunningTask(ctx, task, stream)
	default:
		err := provider.Errorf(provider.ErrorInvalidResponse, "unknown generation task state %s", task.State)
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
}

// handlePendingTask：窗口内 PENDING 可继续轮询（可能正被另一 Pod Submit）；超时无 provider_task_id 则收口 FAILED。
func (s *Service) handlePendingTask(ctx context.Context, task taskstore.Task, stream modelhubv2.ModelHubService_GetGenerationServer) error {
	if strings.TrimSpace(task.ProviderTaskID) == "" && !task.CreatedAt.IsZero() &&
		time.Since(task.CreatedAt) > stalePendingWithoutProviderTaskID {
		return s.failMissingProviderTaskID(ctx, task, stream, "stale pending without provider task id")
	}
	return stream.Send(statusEvent(modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_PENDING, defaultPollAfterMs, nil))
}

// failMissingProviderTaskID 把无上游 id 的异常任务落为 FAILED/SUBMIT_OUTCOME_UNKNOWN，禁止永久轮询。
// expectedState 取读到的精确非终态；条件更新未命中（含 ErrNotFound）一律 Unavailable，不得伪装 FAILED。
func (s *Service) failMissingProviderTaskID(ctx context.Context, task taskstore.Task, stream modelhubv2.ModelHubService_GetGenerationServer, detail string) error {
	persistCtx, cancel := detachPersistContext(ctx)
	defer cancel()
	code := int32(codes.Unavailable)
	message := "submit outcome unknown; " + detail
	reason := string(provider.ErrorSubmitOutcomeUnknown)
	if markErr := s.tasks.MarkFailed(persistCtx, task.TaskID, task.State, code, message, reason); markErr != nil {
		statusErr := provider.ToStatus(provider.Wrap(provider.ErrorUnavailable, "mark missing provider task id failed", markErr))
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	task.State = taskstore.StateFailed
	task.ErrorCode, task.ErrorMessage, task.ErrorReason = code, message, reason
	return stream.Send(statusEvent(modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED, 0, taskRPCStatus(task)))
}

func (s *Service) pollRunningTask(ctx context.Context, task taskstore.Task, stream modelhubv2.ModelHubService_GetGenerationServer) error {
	if strings.TrimSpace(task.ProviderTaskID) == "" {
		// MarkRunning 原子写入 state+id；RUNNING 却无 id 只能是脏数据，立即 FAILED，不等 stale 窗口。
		return s.failMissingProviderTaskID(ctx, task, stream, "running without provider task id")
	}
	video, err := s.videoProvider(task.Provider, task.Model)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	job, err := video.GetVideo(ctx, task.Model, task.ProviderTaskID)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
	switch job.State {
	case provider.VideoJobRunning:
		poll := job.PollAfterMs
		if poll <= 0 {
			poll = defaultPollAfterMs
		}
		return stream.Send(statusEvent(modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_RUNNING, poll, nil))
	case provider.VideoJobFailed:
		code, message, reason := normalizeProviderError(job.Err)
		// 条件更新未命中代表库真相已变；返回 Unavailable 让调用方下次 Get 重读，禁止伪装 FAILED。
		if markErr := s.tasks.MarkFailed(ctx, task.TaskID, taskstore.StateRunning, code, message, reason); markErr != nil {
			statusErr := provider.ToStatus(provider.Wrap(provider.ErrorUnavailable, "mark generation task failed", markErr))
			telemetry.RecordError(ctx, statusErr)
			return statusErr
		}
		task.ErrorCode, task.ErrorMessage, task.ErrorReason = code, message, reason
		// 首次获知模型失败即固定终态与耗时。
		s.recordAsyncVideoModelTerminal(ctx, task, callledger.StatusFailed, job.Err, nil, 0, "", 0)
		return stream.Send(statusEvent(modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED, 0, taskRPCStatus(task)))
	case provider.VideoJobSucceeded:
		if err := s.tasks.MarkSucceeded(ctx, task.TaskID); err != nil {
			statusErr := provider.ToStatus(provider.Wrap(provider.ErrorUnavailable, "mark generation task succeeded", err))
			telemetry.RecordError(ctx, statusErr)
			return statusErr
		}
		// 首次获知模型成功即固定终态/耗时；后续读结果或发送失败只更新交付，不回退。
		s.recordAsyncVideoModelTerminal(ctx, task, callledger.StatusSucceeded, nil, nil, 0, "", 0)
		if err := stream.Send(statusEvent(modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_SUCCEEDED, 0, nil)); err != nil {
			s.recordAsyncVideoDelivery(ctx, task, err, 0, "", 0, nil)
			return err
		}
		return s.streamVideoResult(ctx, task, stream)
	default:
		err := provider.New(provider.ErrorInvalidResponse, "unexpected video job state")
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		return statusErr
	}
}

func (s *Service) streamVideoResult(ctx context.Context, task taskstore.Task, stream modelhubv2.ModelHubService_GetGenerationServer) error {
	video, err := s.videoProvider(task.Provider, task.Model)
	if err != nil {
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		// 模型终态已固定；结果读取配置失败只记交付/结果错误。
		s.recordAsyncVideoDelivery(ctx, task, statusErr, 0, "", 0, nil)
		return statusErr
	}
	var totalBytes int64
	var chunkCount int
	var mimeType string
	var sendErr error
	var lastUsage *modelhubv2.Usage
	emit := func(event *modelhubv2.GenerateEvent) error {
		if event == nil {
			return nil
		}
		// 保留事件中已返回的 usage 到同 task 行；无需新 provider 接口。
		if event.GetUsage() != nil {
			lastUsage = event.GetUsage()
		}
		for _, item := range event.GetItems() {
			if media := item.GetVideo(); media != nil {
				mimeType = media.GetMimeType()
				if data := media.GetData(); len(data) > 0 {
					totalBytes += int64(len(data))
					chunkCount++
				}
			}
		}
		sendErr = stream.Send(&modelhubv2.GenerationTaskEvent{
			Event: &modelhubv2.GenerationTaskEvent_Output{Output: event},
		})
		return sendErr
	}
	if err := video.ReadVideoResult(ctx, task.Model, task.ProviderTaskID, emit); err != nil {
		if sendErr != nil {
			s.recordAsyncVideoDelivery(ctx, task, sendErr, 1, mimeType, totalBytes, lastUsage)
			telemetry.RecordError(ctx, sendErr)
			return sendErr
		}
		statusErr := provider.ToStatus(err)
		telemetry.RecordError(ctx, statusErr)
		// 下载/读结果失败不得把已确认的模型成功改成 failed。
		s.recordAsyncVideoDelivery(ctx, task, statusErr, 0, mimeType, totalBytes, lastUsage)
		return statusErr
	}
	s.recordAsyncVideoDelivery(ctx, task, sendErr, 1, mimeType, totalBytes, lastUsage)
	return sendErr
}

// recordAsyncVideoModelTerminal：首次获知模型终态时固定 status/finished_at/latency；场景取 Submit 已写入值，不读本次 Get metadata。
func (s *Service) recordAsyncVideoModelTerminal(ctx context.Context, task taskstore.Task, status string, providerErr, sendErr error, videoCount int, mimeType string, totalBytes int64) {
	if s.ledger == nil {
		return
	}
	finished := time.Now()
	rec := callledger.Record{
		GenerationTaskID: task.TaskID,
		CallerService:    callledger.NormalizeOrUnknown(task.Caller),
		// 故意不写 BusinessScene：merge 保留 Submit 场景，避免 Get 侧 metadata 覆盖。
		Operation:      callledger.OperationSubmitGeneration,
		Capability:     callledger.CapabilityVideo,
		Model:          task.Model,
		Provider:       task.Provider,
		Status:         status,
		DeliveryStatus: callledger.DeliveryOK,
		StartedAt:      task.CreatedAt,
		FinishedAt:     &finished,
	}
	if !task.CreatedAt.IsZero() {
		rec.LatencyMS = finished.Sub(task.CreatedAt).Milliseconds()
	}
	if sendErr != nil {
		rec.DeliveryStatus = callledger.DeliveryClientSendFailed
	}
	if providerErr != nil {
		rec.ErrorCategory, rec.ErrorCode, rec.ErrorReason, rec.ErrorMessage = callledger.ClassifyError(providerErr)
	}
	if videoCount > 0 || totalBytes > 0 {
		rec.VideoCount = intPtr(videoCount)
		rec.OutputPayload = callledger.BuildVideoOutputSummary(videoCount, totalBytes, mimeType, 0)
	}
	callledger.BestEffort(ctx, s.ledger, rec)
}

// recordAsyncVideoDelivery：结果读取或发送失败只更新交付/输出/usage，不改模型终态、不延长耗时。
func (s *Service) recordAsyncVideoDelivery(ctx context.Context, task taskstore.Task, deliveryErr error, videoCount int, mimeType string, totalBytes int64, usage *modelhubv2.Usage) {
	if s.ledger == nil {
		return
	}
	rec := callledger.Record{
		GenerationTaskID: task.TaskID,
		CallerService:    callledger.NormalizeOrUnknown(task.Caller),
		Operation:        callledger.OperationSubmitGeneration,
		Capability:       callledger.CapabilityVideo,
		Model:            task.Model,
		Provider:         task.Provider,
		// 不传 Status/FinishedAt/Latency：merge 在已终态时忽略回退与延长。
		DeliveryStatus: callledger.DeliveryOK,
		StartedAt:      task.CreatedAt,
	}
	if deliveryErr != nil {
		// 流式回传失败标 client_send_failed；读结果失败只写错误摘要，模型终态仍为成功。
		rec.DeliveryStatus = callledger.DeliveryClientSendFailed
		rec.ErrorCategory, rec.ErrorCode, rec.ErrorReason, rec.ErrorMessage = callledger.ClassifyError(deliveryErr)
	}
	if videoCount > 0 || totalBytes > 0 {
		rec.VideoCount = intPtr(videoCount)
		rec.OutputPayload = callledger.BuildVideoOutputSummary(videoCount, totalBytes, mimeType, 0)
	}
	if usage != nil {
		rec.Usage, rec.UsageDetail = callledger.UsageFromProto(usage)
	}
	callledger.BestEffort(ctx, s.ledger, rec)
}

func (s *Service) videoProvider(providerName, model string) (provider.VideoProvider, error) {
	set, ok := s.providers[providerName]
	if !ok || set.Video == nil {
		return nil, provider.Errorf(provider.ErrorConfiguration, "provider %s video capability missing for model %s", providerName, model)
	}
	return set.Video, nil
}

func callerFromContext(ctx context.Context) string {
	// 公网 API Key 鉴权成功后必须覆盖客户端自报的 x-wg-caller-service，保证幂等命名空间可信。
	if publicCaller, ok := auth.PublicCaller(ctx); ok {
		return publicCaller
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(protocol.CallerMetadataKey)
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func hashGenerateRequest(request *modelhubv2.GenerateRequest) (string, error) {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func publicState(state string) modelhubv2.GenerationTaskState {
	switch state {
	case taskstore.StatePending:
		return modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_PENDING
	case taskstore.StateRunning:
		return modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_RUNNING
	case taskstore.StateSucceeded:
		return modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_SUCCEEDED
	case taskstore.StateFailed:
		return modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED
	default:
		return modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_UNSPECIFIED
	}
}

func statusEvent(state modelhubv2.GenerationTaskState, pollAfterMs int32, rpcErr *rpcstatus.Status) *modelhubv2.GenerationTaskEvent {
	return &modelhubv2.GenerationTaskEvent{
		Event: &modelhubv2.GenerationTaskEvent_Status{
			Status: &modelhubv2.GenerationTaskStatus{
				State:       state,
				PollAfterMs: pollAfterMs,
				Error:       rpcErr,
			},
		},
	}
}

// taskRPCStatus 把已存 error_reason 装入 google.rpc.ErrorInfo（domain=wg.modelhub）。
func taskRPCStatus(task taskstore.Task) *rpcstatus.Status {
	code := task.ErrorCode
	message := task.ErrorMessage
	if code == 0 && message == "" {
		code = int32(codes.Unknown)
		message = "generation failed"
	}
	out := &rpcstatus.Status{Code: code, Message: message}
	reason := strings.TrimSpace(task.ErrorReason)
	if reason == "" {
		reason = string(provider.ErrorUnavailable)
	}
	detail, err := anypb.New(&errdetails.ErrorInfo{
		Reason: reason,
		Domain: errorInfoDomain,
	})
	if err == nil {
		out.Details = append(out.Details, detail)
	}
	return out
}

func normalizeProviderError(err error) (code int32, message, reason string) {
	if err == nil {
		return int32(codes.Unknown), "generation failed", string(provider.ErrorUnavailable)
	}
	st := status.Convert(provider.ToStatus(err))
	return int32(st.Code()), st.Message(), string(provider.Kind(err))
}

func isUncertainSubmit(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return provider.Kind(err) == provider.ErrorTimeout
}

// detachPersistContext 剥离请求取消，保留短超时，专用于已拿到上游 id 后的落库。
func detachPersistContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), persistTimeout)
}
