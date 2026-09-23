package callledger

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	entschema "entgo.io/ent/dialect/sql/schema"
	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/enttest"
	"github.com/wgdl666/wgModelHub/ent/modelcall"

	_ "modernc.org/sqlite"
)

func openLedgerStore(t *testing.T) *Postgres {
	t.Helper()
	db, err := sql.Open("sqlite", "file:model-call-"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`ATTACH DATABASE ':memory:' AS modelhub`); err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(
		t,
		enttest.WithOptions(ent.Driver(drv)),
		enttest.WithMigrateOptions(entschema.WithSchemaName("modelhub")),
	)
	t.Cleanup(func() { _ = client.Close() })
	return NewPostgres(client)
}

func TestPostgresUpsertFreezesTerminalLatencyAndStatus(t *testing.T) {
	store := openLedgerStore(t)
	ctx := context.Background()
	started := time.Now().Add(-5 * time.Second).UTC().Truncate(time.Millisecond)
	finished1 := started.Add(2 * time.Second)
	rec := Record{
		GenerationTaskID: "task-1",
		CallerService:    "wgHub",
		BusinessScene:    "ootd",
		Operation:        OperationSubmitGeneration,
		Capability:       CapabilityVideo,
		Model:            "seedance",
		Provider:         "ark",
		Status:           StatusPending,
		StartedAt:        started,
		InputPayload:     map[string]any{"prompt": "a"},
		VideoResolution:  "720p",
	}
	if err := store.UpsertByGenerationTask(ctx, rec); err != nil {
		t.Fatal(err)
	}

	succ := rec
	succ.Status = StatusSucceeded
	succ.FinishedAt = &finished1
	succ.LatencyMS = 2000
	succ.VideoCount = intPtr(0)
	if err := store.UpsertByGenerationTask(ctx, succ); err != nil {
		t.Fatal(err)
	}

	later := finished1.Add(3 * time.Second)
	failDownload := Record{
		GenerationTaskID: "task-1",
		Status:           StatusFailed,
		FinishedAt:       &later,
		LatencyMS:        9000,
		DeliveryStatus:   DeliveryClientSendFailed,
		ErrorMessage:     "download failed",
		VideoCount:       intPtr(0),
		OutputPayload:    map[string]any{},
		BusinessScene:    "should-not-override",
	}
	if err := store.UpsertByGenerationTask(ctx, failDownload); err != nil {
		t.Fatal(err)
	}

	row, err := store.client.ModelCall.Query().Where(modelcall.GenerationTaskIDEQ("task-1")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusSucceeded {
		t.Fatalf("status=%s want succeeded", row.Status)
	}
	if row.LatencyMs != 2000 {
		t.Fatalf("latency=%d want 2000", row.LatencyMs)
	}
	if row.FinishedAt == nil || row.FinishedAt.UnixMilli() != finished1.UnixMilli() {
		t.Fatalf("finished_at=%v want %v", row.FinishedAt, finished1)
	}
	if row.BusinessScene != "ootd" {
		t.Fatalf("scene=%q", row.BusinessScene)
	}
	if row.DeliveryStatus != DeliveryClientSendFailed {
		t.Fatalf("delivery=%q", row.DeliveryStatus)
	}

	// 补写有实质输出不得被空覆盖；有实质时可更新。
	fill := Record{
		GenerationTaskID: "task-1",
		VideoCount:       intPtr(1),
		OutputPayload:    BuildVideoOutputSummary(1, 12, "video/mp4", 2),
	}
	if err := store.UpsertByGenerationTask(ctx, fill); err != nil {
		t.Fatal(err)
	}
	row, err = store.client.ModelCall.Query().Where(modelcall.GenerationTaskIDEQ("task-1")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.VideoCount == nil || *row.VideoCount != 1 {
		t.Fatalf("video_count=%v", row.VideoCount)
	}
}

func TestPostgresConcurrentFirstWritesKeepOneRow(t *testing.T) {
	store := openLedgerStore(t)
	ctx := context.Background()
	started := time.Now().UTC()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			finished := started.Add(time.Duration(i+1) * time.Second)
			status := StatusPending
			payload := map[string]any{}
			if i == 1 {
				status = StatusSucceeded
				payload = BuildVideoOutputSummary(1, 8, "video/mp4", 1)
			}
			errs <- store.UpsertByGenerationTask(ctx, Record{
				GenerationTaskID: "race-task",
				CallerService:    "wgHub",
				BusinessScene:    "scene",
				Operation:        OperationSubmitGeneration,
				Capability:       CapabilityVideo,
				Model:            "m",
				Provider:         "p",
				Status:           status,
				StartedAt:        started,
				FinishedAt:       &finished,
				LatencyMS:        int64((i + 1) * 1000),
				OutputPayload:    payload,
				VideoCount:       intPtr(i),
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.client.ModelCall.Query().Where(modelcall.GenerationTaskIDEQ("race-task")).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows=%d", n)
	}
}

// TestPostgresOptimisticLockTwoPendingReaders：控制交错使两写者先读到同一 pending 再写，
// 验证终态不倒退且耗时只写一次（覆盖仅「双首次 create」测不到的 bug）。
func TestPostgresOptimisticLockTwoPendingReaders(t *testing.T) {
	store := openLedgerStore(t)
	ctx := context.Background()
	started := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.UpsertByGenerationTask(ctx, Record{
		GenerationTaskID: "ol-task",
		CallerService:    "wgHub",
		BusinessScene:    "ootd",
		Operation:        OperationSubmitGeneration,
		Capability:       CapabilityVideo,
		Model:            "m",
		Provider:         "p",
		Status:           StatusPending,
		StartedAt:        started,
	}); err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	store.afterReadForTest = func(existing *ent.ModelCall) {
		// 只在首次双读 pending 时同步；CAS 失败重读到终态后不再挡，避免死锁。
		if existing.Status != StatusPending {
			return
		}
		select {
		case entered <- struct{}{}:
			<-release
		default:
			<-release
		}
	}

	finishedSucc := started.Add(2 * time.Second)
	finishedLate := started.Add(9 * time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- store.UpsertByGenerationTask(ctx, Record{
			GenerationTaskID: "ol-task",
			Status:           StatusSucceeded,
			FinishedAt:       &finishedSucc,
			LatencyMS:        2000,
			VideoCount:       intPtr(1),
			OutputPayload:    BuildVideoOutputSummary(1, 4, "video/mp4", 1),
		})
	}()
	go func() {
		defer wg.Done()
		// 另一写者先读到 pending 后写迟到 pending（或失败），不得覆写先到终态。
		errs <- store.UpsertByGenerationTask(ctx, Record{
			GenerationTaskID: "ol-task",
			Status:           StatusPending,
			FinishedAt:       &finishedLate,
			LatencyMS:        9000,
		})
	}()

	for i := 0; i < 2; i++ {
		<-entered
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	row, err := store.client.ModelCall.Query().Where(modelcall.GenerationTaskIDEQ("ol-task")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusSucceeded {
		t.Fatalf("status=%s want succeeded", row.Status)
	}
	if row.LatencyMs != 2000 {
		t.Fatalf("latency=%d want 2000 (must not be overwritten)", row.LatencyMs)
	}
	if row.FinishedAt == nil || row.FinishedAt.UnixMilli() != finishedSucc.UnixMilli() {
		t.Fatalf("finished_at=%v want %v", row.FinishedAt, finishedSucc)
	}
}

func TestMergeVideoRecordLatePendingDoesNotOverrideTerminal(t *testing.T) {
	finished := time.Now()
	prev := Record{Status: StatusSucceeded, FinishedAt: &finished, LatencyMS: 100, BusinessScene: "ootd", OutputPayload: map[string]any{"video_count": 1}}
	next := Record{Status: StatusPending, LatencyMS: 999, BusinessScene: "other", OutputPayload: map[string]any{}}
	got := mergeVideoRecord(prev, next)
	if got.Status != StatusSucceeded || got.LatencyMS != 100 || got.BusinessScene != "ootd" {
		t.Fatalf("%+v", got)
	}
	if len(got.OutputPayload) == 0 {
		t.Fatal("empty next must not wipe output")
	}
}

func intPtr(v int) *int { return &v }
