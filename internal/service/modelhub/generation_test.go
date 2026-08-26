package modelhub

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/internal/taskstore"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// memoryStore 仅用于单测模拟跨调用持久化，生产路径必须用 PostgreSQL。
type memoryStore struct {
	mu    sync.Mutex
	byID  map[string]taskstore.Task
	byKey map[string]string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{byID: map[string]taskstore.Task{}, byKey: map[string]string{}}
}

func (m *memoryStore) key(caller, requestID string) string {
	return caller + "\x00" + requestID
}

func (m *memoryStore) InsertPending(_ context.Context, task taskstore.Task) (taskstore.Task, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(task.Caller, task.RequestID)
	if id, ok := m.byKey[k]; ok {
		existing := m.byID[id]
		if existing.RequestHash != task.RequestHash {
			return taskstore.Task{}, false, taskstore.ErrRequestHashConflict
		}
		return existing, false, nil
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	task.UpdatedAt = task.CreatedAt
	m.byKey[k] = task.TaskID
	m.byID[task.TaskID] = task
	return task, true, nil
}

func (m *memoryStore) GetByTaskID(_ context.Context, taskID string) (taskstore.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.byID[taskID]
	if !ok {
		return taskstore.Task{}, taskstore.ErrNotFound
	}
	return task, nil
}

func (m *memoryStore) MarkRunning(_ context.Context, taskID, providerName, providerTaskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.byID[taskID]
	if !ok || task.State != taskstore.StatePending {
		return taskstore.ErrNotFound
	}
	task.Provider = providerName
	task.ProviderTaskID = providerTaskID
	task.State = taskstore.StateRunning
	task.UpdatedAt = time.Now()
	m.byID[taskID] = task
	return nil
}

func (m *memoryStore) MarkFailed(_ context.Context, taskID, expectedState string, code int32, message, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.byID[taskID]
	// 必须与读到的精确非终态一致，避免过期 PENDING 收口覆盖已 RUNNING 的有效任务。
	if !ok || task.State != expectedState {
		return taskstore.ErrNotFound
	}
	task.State = taskstore.StateFailed
	task.ErrorCode = code
	task.ErrorMessage = message
	task.ErrorReason = reason
	task.UpdatedAt = time.Now()
	m.byID[taskID] = task
	return nil
}

func (m *memoryStore) MarkSucceeded(_ context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.byID[taskID]
	if !ok || task.State != taskstore.StateRunning {
		return taskstore.ErrNotFound
	}
	task.State = taskstore.StateSucceeded
	task.ErrorCode = 0
	task.ErrorMessage = ""
	task.ErrorReason = ""
	task.UpdatedAt = time.Now()
	m.byID[taskID] = task
	return nil
}

type probeStore struct {
	*memoryStore
	markRunningCtxErr  error
	markRunningErr     error
	lastMarkRunningCtx context.Context
}

func (p *probeStore) MarkRunning(ctx context.Context, taskID, providerName, providerTaskID string) error {
	p.lastMarkRunningCtx = ctx
	p.markRunningCtxErr = ctx.Err()
	if p.markRunningErr != nil {
		return p.markRunningErr
	}
	return p.memoryStore.MarkRunning(ctx, taskID, providerName, providerTaskID)
}

type fakeVideo struct {
	submitCount int
	job         provider.VideoJob
	result      []byte
	submitErr   error
}

func (f *fakeVideo) SubmitVideo(context.Context, string, *modelhubv2.GenerateRequest) (string, error) {
	f.submitCount++
	if f.submitErr != nil {
		return "", f.submitErr
	}
	return "provider-task-1", nil
}

func (f *fakeVideo) GetVideo(context.Context, string, string) (provider.VideoJob, error) {
	return f.job, nil
}

func (f *fakeVideo) ReadVideoResult(_ context.Context, _ string, _ string, emit provider.EmitEvent) error {
	return provider.EmitVideoChunksFromReader(strings.NewReader(string(f.result)), "video/mp4", "provider-task-1", 0, emit)
}

func (f *fakeVideo) GenerateVideo(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) error {
	return provider.RunVideoJob(ctx, f, model, request, emit)
}

type generationStreamRecorder struct {
	grpc.ServerStream
	ctx    context.Context
	events []*modelhubv2.GenerationTaskEvent
}

func (r *generationStreamRecorder) Context() context.Context { return r.ctx }

func (r *generationStreamRecorder) Send(event *modelhubv2.GenerationTaskEvent) error {
	r.events = append(r.events, event)
	return nil
}

func videoService(video provider.VideoProvider, store taskstore.Store) *Service {
	return New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ltx": {Models: []string{"ltx"}, LTX: &config.LTXProviderConfig{
				BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
			}},
		},
	}, map[string]provider.Set{"ltx": {Video: video}}, store)
}

func videoSubmitRequest(model, requestID string) *modelhubv2.SubmitGenerationRequest {
	return &modelhubv2.SubmitGenerationRequest{
		RequestId: requestID,
		Request: &modelhubv2.GenerateRequest{
			Model: model,
			Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{
						{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
							MimeType: "image/png",
							Source:   &modelhubv2.Media_Uri{Uri: "https://example.com/frame.png"},
						}}},
						{Content: &modelhubv2.ContentPart_Text{Text: "walk"}},
					},
				}},
			}}},
			Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Video{Video: &modelhubv2.VideoOutput{}}},
		},
	}
}

func TestSubmitGenerationRejectsNonVideo(t *testing.T) {
	service := New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"ark": {Models: []string{"chat-model"}, Ark: &config.ArkProviderConfig{APIKey: "k"}},
		},
	}, map[string]provider.Set{"ark": {Text: &recordingText{}}}, newMemoryStore())

	_, err := service.SubmitGeneration(context.Background(), &modelhubv2.SubmitGenerationRequest{
		RequestId: "r1",
		Request:   textRequest("chat-model", "hi"),
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestSubmitGenerationIdempotentSameHash(t *testing.T) {
	video := &fakeVideo{job: provider.VideoJob{State: provider.VideoJobRunning, PollAfterMs: 1000}}
	store := newMemoryStore()
	service := videoService(video, store)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub"))
	first, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.GetTaskId() != second.GetTaskId() {
		t.Fatalf("task ids differ: %s vs %s", first.GetTaskId(), second.GetTaskId())
	}
	if video.submitCount != 1 {
		t.Fatalf("submitCount=%d", video.submitCount)
	}
}

// TestSubmitGenerationPersistsModelRouteSelectedProvider：多 provider 声明同一视频模型时，
// 落库 Provider 必须是 model_routes 显式选定实例，不能落成 map 遍历到的第一个声明方。
func TestSubmitGenerationPersistsModelRouteSelectedProvider(t *testing.T) {
	primary := &fakeVideo{}
	backup := &fakeVideo{}
	store := newMemoryStore()
	ltxCfg := &config.LTXProviderConfig{
		BaseURL: "https://x", Duration: 1, FPS: 1, PollInterval: 1, MaxPollTime: 1,
	}
	const modelID = "shared-video"
	service := New(config.Config{
		Providers: map[string]config.ProviderConfig{
			"video_primary": {Models: []string{modelID}, LTX: ltxCfg},
			"video_backup":  {Models: []string{modelID}, LTX: ltxCfg},
		},
		ModelRouteOverrides: map[string]string{modelID: "video_backup"},
	}, map[string]provider.Set{
		"video_primary": {Video: primary},
		"video_backup":  {Video: backup},
	}, store)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub"))
	task, err := service.SubmitGeneration(ctx, videoSubmitRequest(modelID, "req-route"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetByTaskID(ctx, task.GetTaskId())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Provider != "video_backup" {
		t.Fatalf("Provider=%q want video_backup (model_routes selection)", stored.Provider)
	}
	if primary.submitCount != 0 || backup.submitCount != 1 {
		t.Fatalf("primary.submit=%d backup.submit=%d", primary.submitCount, backup.submitCount)
	}
}

func TestSubmitGenerationConflictDifferentHash(t *testing.T) {
	video := &fakeVideo{job: provider.VideoJob{State: provider.VideoJobRunning, PollAfterMs: 1000}}
	service := videoService(video, newMemoryStore())

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub"))
	if _, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-2")); err != nil {
		t.Fatal(err)
	}
	conflict := videoSubmitRequest("ltx", "req-2")
	conflict.Request.Input.Items[0].GetMessage().Parts[1] = &modelhubv2.ContentPart{
		Content: &modelhubv2.ContentPart_Text{Text: "different"},
	}
	_, err := service.SubmitGeneration(ctx, conflict)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestSubmitUncertainOutcomeMarksFailedNoRetry(t *testing.T) {
	video := &fakeVideo{submitErr: provider.New(provider.ErrorTimeout, "upstream timed out")}
	store := newMemoryStore()
	service := videoService(video, store)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub"))

	first, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-uncertain"))
	if err != nil {
		t.Fatal(err)
	}
	if first.GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED {
		t.Fatalf("state=%v", first.GetState())
	}
	stored, err := store.GetByTaskID(ctx, first.GetTaskId())
	if err != nil {
		t.Fatal(err)
	}
	if stored.ErrorReason != string(provider.ErrorSubmitOutcomeUnknown) || stored.ProviderTaskID != "" {
		t.Fatalf("stored=%#v", stored)
	}

	second, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-uncertain"))
	if err != nil {
		t.Fatal(err)
	}
	if second.GetTaskId() != first.GetTaskId() || second.GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED {
		t.Fatalf("second=%#v", second)
	}
	if video.submitCount != 1 {
		t.Fatalf("submitCount=%d want 1 (no auto-retry)", video.submitCount)
	}
}

func TestSubmitMarkRunningUsesDetachedContext(t *testing.T) {
	video := &fakeVideo{}
	store := &probeStore{memoryStore: newMemoryStore()}
	service := videoService(video, store)

	ctx, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub")))
	cancel() // 请求已取消；落库必须用 detached context，否则会丢掉已返回的 provider id。

	task, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-detach"))
	if err != nil {
		t.Fatal(err)
	}
	if task.GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_RUNNING {
		t.Fatalf("state=%v", task.GetState())
	}
	if store.markRunningCtxErr != nil {
		t.Fatalf("MarkRunning saw canceled ctx: %v", store.markRunningCtxErr)
	}
	if _, ok := store.lastMarkRunningCtx.Deadline(); !ok {
		t.Fatal("expected short deadline on persist context")
	}
}

func TestSubmitMarkRunningDBFailureReturnsUnavailable(t *testing.T) {
	video := &fakeVideo{}
	store := &probeStore{memoryStore: newMemoryStore(), markRunningErr: errors.New("db down")}
	service := videoService(video, store)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub"))

	_, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-db"))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
}

func TestGetStalePendingWithoutProviderTaskIDFails(t *testing.T) {
	store := newMemoryStore()
	service := videoService(&fakeVideo{}, store)
	ctx := context.Background()
	taskID := "stale-1"
	_, _, err := store.InsertPending(ctx, taskstore.Task{
		TaskID:      taskID,
		Caller:      "wg-hub",
		RequestID:   "stale-req",
		RequestHash: "h",
		Model:       "ltx",
		Provider:    "ltx",
		State:       taskstore.StatePending,
		CreatedAt:   time.Now().Add(-stalePendingWithoutProviderTaskID - time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	stream := &generationStreamRecorder{ctx: ctx}
	if err := service.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: taskID}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 1 || stream.events[0].GetStatus().GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED {
		t.Fatalf("events=%#v", stream.events)
	}
	info := errorInfoFromStatus(stream.events[0].GetStatus().GetError())
	if info == nil || info.GetReason() != string(provider.ErrorSubmitOutcomeUnknown) {
		t.Fatalf("errorinfo=%#v", info)
	}
	stored, _ := store.GetByTaskID(ctx, taskID)
	if stored.State != taskstore.StateFailed {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestGetFreshPendingWithoutProviderTaskIDStillPending(t *testing.T) {
	// 窗口内可能仍有 Pod 正在 Submit（含 Gemini Files 最长 10 分钟），不得提前 FAILED。
	if stalePendingWithoutProviderTaskID <= geminiFilesSubmitWindow {
		t.Fatalf("stale window %v must exceed geminiFilesSubmitWindow %v", stalePendingWithoutProviderTaskID, geminiFilesSubmitWindow)
	}
	store := newMemoryStore()
	service := videoService(&fakeVideo{}, store)
	ctx := context.Background()
	taskID := "fresh-pending"
	_, _, err := store.InsertPending(ctx, taskstore.Task{
		TaskID: taskID, Caller: "wg-hub", RequestID: "fresh-req", RequestHash: "h",
		Model: "ltx", Provider: "ltx", State: taskstore.StatePending,
		CreatedAt: time.Now().Add(-(geminiFilesSubmitWindow + time.Minute)),
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := &generationStreamRecorder{ctx: ctx}
	if err := service.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: taskID}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 1 || stream.events[0].GetStatus().GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_PENDING {
		t.Fatalf("events=%#v", stream.events)
	}
}

func TestGetRunningWithoutProviderTaskIDFailsImmediately(t *testing.T) {
	store := newMemoryStore()
	service := videoService(&fakeVideo{}, store)
	ctx := context.Background()
	taskID := "running-no-id"
	_, _, err := store.InsertPending(ctx, taskstore.Task{
		TaskID: taskID, Caller: "wg-hub", RequestID: "bad-running", RequestHash: "h",
		Model: "ltx", Provider: "ltx", State: taskstore.StatePending, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	task := store.byID[taskID]
	task.State = taskstore.StateRunning
	task.ProviderTaskID = ""
	store.byID[taskID] = task
	store.mu.Unlock()

	stream := &generationStreamRecorder{ctx: ctx}
	if err := service.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: taskID}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 1 || stream.events[0].GetStatus().GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED {
		t.Fatalf("events=%#v", stream.events)
	}
	info := errorInfoFromStatus(stream.events[0].GetStatus().GetError())
	if info == nil || info.GetReason() != string(provider.ErrorSubmitOutcomeUnknown) {
		t.Fatalf("errorinfo=%#v", info)
	}
}

func TestGetGenerationRunningAndSucceeded(t *testing.T) {
	video := &fakeVideo{
		job:    provider.VideoJob{State: provider.VideoJobRunning, PollAfterMs: 1500},
		result: []byte("video-bytes"),
	}
	store := newMemoryStore()
	service := videoService(video, store)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub"))
	task, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-3"))
	if err != nil {
		t.Fatal(err)
	}
	if task.GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_RUNNING {
		t.Fatalf("state=%v", task.GetState())
	}

	runningStream := &generationStreamRecorder{ctx: ctx}
	if err := service.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: task.GetTaskId()}, runningStream); err != nil {
		t.Fatal(err)
	}
	if len(runningStream.events) != 1 || runningStream.events[0].GetStatus().GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_RUNNING {
		t.Fatalf("events=%#v", runningStream.events)
	}
	if runningStream.events[0].GetStatus().GetPollAfterMs() != 1500 {
		t.Fatalf("poll=%d", runningStream.events[0].GetStatus().GetPollAfterMs())
	}

	video.job = provider.VideoJob{State: provider.VideoJobSucceeded}
	okStream := &generationStreamRecorder{ctx: ctx}
	if err := service.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: task.GetTaskId()}, okStream); err != nil {
		t.Fatal(err)
	}
	if len(okStream.events) < 2 {
		t.Fatalf("events=%#v", okStream.events)
	}
	if okStream.events[0].GetStatus().GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_SUCCEEDED {
		t.Fatalf("first=%#v", okStream.events[0])
	}
	if okStream.events[1].GetOutput() == nil || len(okStream.events[1].GetOutput().GetItems()) == 0 {
		t.Fatalf("missing output %#v", okStream.events)
	}
	stored, err := store.GetByTaskID(ctx, task.GetTaskId())
	if err != nil || stored.State != taskstore.StateSucceeded {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestGetGenerationFailedIncludesErrorInfo(t *testing.T) {
	video := &fakeVideo{
		job: provider.VideoJob{
			State: provider.VideoJobFailed,
			Err:   provider.New(provider.ErrorUnavailable, "upstream failed"),
		},
	}
	store := newMemoryStore()
	service := videoService(video, store)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(protocol.CallerMetadataKey, "wg-hub"))
	task, err := service.SubmitGeneration(ctx, videoSubmitRequest("ltx", "req-4"))
	if err != nil {
		t.Fatal(err)
	}
	stream := &generationStreamRecorder{ctx: ctx}
	if err := service.GetGeneration(&modelhubv2.GetGenerationRequest{TaskId: task.GetTaskId()}, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.events) != 1 || stream.events[0].GetStatus().GetState() != modelhubv2.GenerationTaskState_GENERATION_TASK_STATE_FAILED {
		t.Fatalf("events=%#v", stream.events)
	}
	info := errorInfoFromStatus(stream.events[0].GetStatus().GetError())
	if info == nil || info.GetDomain() != "wg.modelhub" || info.GetReason() != string(provider.ErrorUnavailable) {
		t.Fatalf("errorinfo=%#v", info)
	}
}

func TestMemoryStoreRejectsTerminalOverwrite(t *testing.T) {
	store := newMemoryStore()
	ctx := context.Background()
	task, _, err := store.InsertPending(ctx, taskstore.Task{
		TaskID: "t1", Caller: "c", RequestID: "r", RequestHash: "h", Model: "m", Provider: "p", State: taskstore.StatePending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, task.TaskID, taskstore.StatePending, 14, "failed", string(provider.ErrorSubmitOutcomeUnknown)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(ctx, task.TaskID, "p", "id"); !errors.Is(err, taskstore.ErrNotFound) {
		t.Fatalf("MarkRunning after FAILED: %v", err)
	}
	if err := store.MarkFailed(ctx, task.TaskID, taskstore.StatePending, 1, "again", "x"); !errors.Is(err, taskstore.ErrNotFound) {
		t.Fatalf("MarkFailed overwrite: %v", err)
	}
}

// TestMarkFailedExpectedPendingDoesNotOverwriteRunning：过期 PENDING 收口与先发生的 MarkRunning 竞争时不得误杀。
func TestMarkFailedExpectedPendingDoesNotOverwriteRunning(t *testing.T) {
	store := newMemoryStore()
	ctx := context.Background()
	task, _, err := store.InsertPending(ctx, taskstore.Task{
		TaskID: "race-1", Caller: "c", RequestID: "r-race", RequestHash: "h", Model: "m", Provider: "p", State: taskstore.StatePending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(ctx, task.TaskID, "p", "provider-id"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, task.TaskID, taskstore.StatePending, 14, "stale", string(provider.ErrorSubmitOutcomeUnknown)); !errors.Is(err, taskstore.ErrNotFound) {
		t.Fatalf("MarkFailed expected PENDING after RUNNING: %v", err)
	}
	stored, err := store.GetByTaskID(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != taskstore.StateRunning || stored.ProviderTaskID != "provider-id" {
		t.Fatalf("stored=%#v", stored)
	}
}

// TestGetStalePendingAfterMarkRunningReturnsUnavailable：状态冲突不得伪装 FAILED event。
func TestGetStalePendingAfterMarkRunningReturnsUnavailable(t *testing.T) {
	store := newMemoryStore()
	service := videoService(&fakeVideo{}, store)
	ctx := context.Background()
	taskID := "stale-race"
	_, _, err := store.InsertPending(ctx, taskstore.Task{
		TaskID: taskID, Caller: "wg-hub", RequestID: "stale-race-req", RequestHash: "h",
		Model: "ltx", Provider: "ltx", State: taskstore.StatePending,
		CreatedAt: time.Now().Add(-stalePendingWithoutProviderTaskID - time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(ctx, taskID, "ltx", "provider-alive"); err != nil {
		t.Fatal(err)
	}
	// 模拟 Get 读到过期 PENDING 快照后才 MarkRunning 的竞态：直接调 failMissingProviderTaskID。
	stream := &generationStreamRecorder{ctx: ctx}
	staleSnap := taskstore.Task{
		TaskID: taskID, State: taskstore.StatePending, CreatedAt: time.Now().Add(-stalePendingWithoutProviderTaskID - time.Minute),
	}
	err = service.failMissingProviderTaskID(ctx, staleSnap, stream, "stale pending without provider task id")
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
	if len(stream.events) != 0 {
		t.Fatalf("must not emit fake FAILED events=%#v", stream.events)
	}
	stored, _ := store.GetByTaskID(ctx, taskID)
	if stored.State != taskstore.StateRunning {
		t.Fatalf("stored=%#v", stored)
	}
}

// TestGetTerminalUpdateConflictReturnsUnavailable：poll 路径终态条件更新冲突不得伪装 SUCCEEDED/FAILED。
func TestGetTerminalUpdateConflictReturnsUnavailable(t *testing.T) {
	video := &fakeVideo{job: provider.VideoJob{State: provider.VideoJobSucceeded}}
	store := newMemoryStore()
	service := videoService(video, store)
	ctx := context.Background()
	taskID := "conflict-terminal"
	_, _, err := store.InsertPending(ctx, taskstore.Task{
		TaskID: taskID, Caller: "wg-hub", RequestID: "req-conflict", RequestHash: "h",
		Model: "ltx", Provider: "ltx", State: taskstore.StatePending, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRunning(ctx, taskID, "ltx", "provider-task-1"); err != nil {
		t.Fatal(err)
	}
	// 库已 SUCCEEDED；持 RUNNING 快照的 poll 不得伪装成功事件。
	if err := store.MarkSucceeded(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	snap := taskstore.Task{TaskID: taskID, Provider: "ltx", Model: "ltx", ProviderTaskID: "provider-task-1", State: taskstore.StateRunning}
	okStream := &generationStreamRecorder{ctx: ctx}
	err = service.pollRunningTask(ctx, snap, okStream)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("succeeded conflict code=%v err=%v", status.Code(err), err)
	}
	if len(okStream.events) != 0 {
		t.Fatalf("must not emit fake SUCCEEDED events=%#v", okStream.events)
	}

	// 库仍为 SUCCEEDED；上游回报 FAILED 时 MarkFailed(expected RUNNING) 冲突，不得伪装 FAILED。
	video.job = provider.VideoJob{
		State: provider.VideoJobFailed,
		Err:   provider.New(provider.ErrorUnavailable, "upstream failed"),
	}
	failStream := &generationStreamRecorder{ctx: ctx}
	err = service.pollRunningTask(ctx, snap, failStream)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("failed conflict code=%v err=%v", status.Code(err), err)
	}
	if len(failStream.events) != 0 {
		t.Fatalf("must not emit fake FAILED events=%#v", failStream.events)
	}
	stored, _ := store.GetByTaskID(ctx, taskID)
	if stored.State != taskstore.StateSucceeded {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestEmitVideoChunksFromReaderEnforcesMax(t *testing.T) {
	err := provider.EmitVideoChunksFromReader(io.LimitReader(strings.NewReader(strings.Repeat("a", protocol.MaxVideoBytes+2)), int64(protocol.MaxVideoBytes+2)), "video/mp4", "id", 0, nil)
	if err == nil {
		t.Fatal("expected max bytes error")
	}
}

func errorInfoFromStatus(st *rpcstatus.Status) *errdetails.ErrorInfo {
	if st == nil {
		return nil
	}
	for _, d := range st.GetDetails() {
		info := &errdetails.ErrorInfo{}
		if err := d.UnmarshalTo(info); err == nil && info.GetReason() != "" {
			return info
		}
	}
	return nil
}
