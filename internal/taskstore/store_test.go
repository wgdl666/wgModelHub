package taskstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/enttest"

	_ "modernc.org/sqlite"
)

// 测试用 enttest 仅在临时 SQLite 上自动建表；生产路径不得 Schema.Create。
// 用纯 Go SQLite（CGO_ENABLED=0 可用），经 OpenDB 注入 dialect.SQLite，保证 Ent 仍按 SQLite 方言生成语句。
func openTestStore(t *testing.T) *Postgres {
	t.Helper()
	db, err := sql.Open("sqlite", "file:gen-task-"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(ent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return NewPostgres(client)
}

func sampleTask(taskID, caller, requestID, hash string) Task {
	return Task{
		TaskID:      taskID,
		Caller:      caller,
		RequestID:   requestID,
		RequestHash: hash,
		Model:       "m",
		Provider:    "p",
		State:       StatePending,
	}
}

func TestInsertPending_FirstCreate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	stored, created, err := store.InsertPending(ctx, sampleTask("t1", "c", "r1", "h1"))
	if err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if !created {
		t.Fatal("expected created=true")
	}
	if stored.TaskID != "t1" || stored.State != StatePending || stored.RequestHash != "h1" {
		t.Fatalf("unexpected stored: %+v", stored)
	}
	if stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("timestamps should be set: %+v", stored)
	}
}

func TestInsertPending_SameHashIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	first, _, err := store.InsertPending(ctx, sampleTask("t1", "c", "r1", "h1"))
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	second, created, err := store.InsertPending(ctx, sampleTask("t2", "c", "r1", "h1"))
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if created {
		t.Fatal("expected created=false on same hash")
	}
	if second.TaskID != first.TaskID || second.RequestHash != first.RequestHash {
		t.Fatalf("idempotent mismatch: first=%+v second=%+v", first, second)
	}
}

func TestInsertPending_DifferentHashConflict(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, _, err := store.InsertPending(ctx, sampleTask("t1", "c", "r1", "h1")); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, _, err := store.InsertPending(ctx, sampleTask("t2", "c", "r1", "h2"))
	if !errors.Is(err, ErrRequestHashConflict) {
		t.Fatalf("want ErrRequestHashConflict, got %v", err)
	}
}

func TestGetByTaskID(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, _, err := store.InsertPending(ctx, sampleTask("t1", "c", "r1", "h1")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := store.GetByTaskID(ctx, "t1")
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if got.Caller != "c" || got.RequestID != "r1" {
		t.Fatalf("unexpected: %+v", got)
	}
	if _, err := store.GetByTaskID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMarkRunning_AndSucceeded(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, _, err := store.InsertPending(ctx, sampleTask("t1", "c", "r1", "h1")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	before, err := store.GetByTaskID(ctx, "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	if err := store.MarkRunning(ctx, "t1", "prov", "ptid"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	running, err := store.GetByTaskID(ctx, "t1")
	if err != nil {
		t.Fatalf("get running: %v", err)
	}
	if running.State != StateRunning || running.Provider != "prov" || running.ProviderTaskID != "ptid" {
		t.Fatalf("unexpected running: %+v", running)
	}
	if !running.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("updated_at should advance: before=%v after=%v", before.UpdatedAt, running.UpdatedAt)
	}

	if err := store.MarkSucceeded(ctx, "t1"); err != nil {
		t.Fatalf("MarkSucceeded: %v", err)
	}
	done, err := store.GetByTaskID(ctx, "t1")
	if err != nil {
		t.Fatalf("get done: %v", err)
	}
	if done.State != StateSucceeded {
		t.Fatalf("want SUCCEEDED, got %s", done.State)
	}
}

func TestMarkFailed_FromPendingAndRunning(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, _, err := store.InsertPending(ctx, sampleTask("t1", "c", "r1", "h1")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := store.MarkFailed(ctx, "t1", StatePending, 14, "boom", "reason"); err != nil {
		t.Fatalf("MarkFailed pending: %v", err)
	}
	failed, err := store.GetByTaskID(ctx, "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if failed.State != StateFailed || failed.ErrorCode != 14 || failed.ErrorMessage != "boom" || failed.ErrorReason != "reason" {
		t.Fatalf("unexpected failed: %+v", failed)
	}

	if _, _, err := store.InsertPending(ctx, sampleTask("t2", "c", "r2", "h2")); err != nil {
		t.Fatalf("insert t2: %v", err)
	}
	if err := store.MarkRunning(ctx, "t2", "p", "id"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.MarkFailed(ctx, "t2", StateRunning, 1, "fail", "r"); err != nil {
		t.Fatalf("MarkFailed running: %v", err)
	}
}

func TestConditionalUpdates_WrongExpectedOrTerminal(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, _, err := store.InsertPending(ctx, sampleTask("t1", "c", "r1", "h1")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// 错误 expectedState：PENDING 任务不能用 RUNNING 期望失败。
	if err := store.MarkFailed(ctx, "t1", StateRunning, 1, "x", "y"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for wrong expected, got %v", err)
	}
	if err := store.MarkSucceeded(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for PENDING→SUCCEEDED, got %v", err)
	}

	if err := store.MarkFailed(ctx, "t1", StatePending, 1, "done", "r"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	// 终态不可复活。
	if err := store.MarkRunning(ctx, "t1", "p", "id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound resurrecting FAILED, got %v", err)
	}
	if err := store.MarkFailed(ctx, "t1", StatePending, 2, "again", "r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound re-failing terminal, got %v", err)
	}
	if err := store.MarkSucceeded(ctx, "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound FAILED→SUCCEEDED, got %v", err)
	}
}

func TestConditionalUpdates_UnknownTaskID(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if err := store.MarkRunning(ctx, "missing", "p", "id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := store.MarkFailed(ctx, "missing", StatePending, 1, "m", "r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkFailed: %v", err)
	}
	if err := store.MarkSucceeded(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkSucceeded: %v", err)
	}
}

func TestInsertPending_TaskIDCollisionNotIdempotent(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	if _, _, err := store.InsertPending(ctx, sampleTask("same-id", "c1", "r1", "h1")); err != nil {
		t.Fatalf("first: %v", err)
	}
	// 同一 task_id、不同 (caller, request_id)：应是主键冲突，不能伪装成幂等命中。
	_, created, err := store.InsertPending(ctx, sampleTask("same-id", "c2", "r2", "h2"))
	if err == nil || created {
		t.Fatalf("want constraint error without idempotent success, got created=%v err=%v", created, err)
	}
	if errors.Is(err, ErrRequestHashConflict) || errors.Is(err, ErrNotFound) {
		t.Fatalf("should surface raw constraint error, got %v", err)
	}
}
