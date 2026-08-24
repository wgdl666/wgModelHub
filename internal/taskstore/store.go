package taskstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/generationtask"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// 与对外 GenerationTaskState 对齐的本地持久化状态；不含供应商语义字段外泄。
const (
	StatePending   = "PENDING"
	StateRunning   = "RUNNING"
	StateSucceeded = "SUCCEEDED"
	StateFailed    = "FAILED"
)

var (
	ErrNotFound            = errors.New("generation task not found")
	ErrRequestHashConflict = errors.New("generation request hash conflict")
)

// Task 是跨 Pod 可查询的最小技术事实；不保存 prompt、媒体或最终视频。
type Task struct {
	TaskID         string
	Caller         string
	RequestID      string
	RequestHash    string
	Model          string
	Provider       string
	ProviderTaskID string
	State          string
	ErrorCode      int32
	ErrorMessage   string
	ErrorReason    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Store 只服务视频长任务 Submit/Get；前台 Generate 不得经过本接口。
type Store interface {
	InsertPending(ctx context.Context, task Task) (stored Task, created bool, err error)
	GetByTaskID(ctx context.Context, taskID string) (Task, error)
	MarkRunning(ctx context.Context, taskID, provider, providerTaskID string) error
	// MarkFailed 要求 expectedState 为读到的精确非终态（PENDING 或 RUNNING）；状态已变则 ErrNotFound。
	MarkFailed(ctx context.Context, taskID, expectedState string, code int32, message, reason string) error
	MarkSucceeded(ctx context.Context, taskID string) error
}

// Postgres 经 Ent 访问 generation_task；不做启动 DDL。
type Postgres struct {
	client *ent.Client
}

func NewPostgres(client *ent.Client) *Postgres {
	return &Postgres{client: client}
}

// Open 用 Ent + pgx stdlib 建连；显式 Ping，失败时关闭底层连接。
func Open(ctx context.Context, dsn string) (*ent.Client, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	drv := entsql.OpenDB(dialect.Postgres, db)
	return ent.NewClient(ent.Driver(drv)), nil
}

func (p *Postgres) InsertPending(ctx context.Context, task Task) (Task, bool, error) {
	// 并发安全依赖库唯一键；冲突后只按 (caller, request_id) 回查，避免 task_id 等其它冲突被当成幂等成功。
	created, err := p.client.GenerationTask.Create().
		SetID(task.TaskID).
		SetCaller(task.Caller).
		SetRequestID(task.RequestID).
		SetRequestHash(task.RequestHash).
		SetModel(task.Model).
		SetProvider(task.Provider).
		SetProviderTaskID("").
		SetState(StatePending).
		SetErrorCode(0).
		SetErrorMessage("").
		SetErrorReason("").
		Save(ctx)
	if err == nil {
		return fromEnt(created), true, nil
	}
	if !ent.IsConstraintError(err) {
		return Task{}, false, err
	}
	existing, getErr := p.getByCallerRequest(ctx, task.Caller, task.RequestID)
	if getErr != nil {
		if errors.Is(getErr, ErrNotFound) {
			return Task{}, false, err
		}
		return Task{}, false, getErr
	}
	if existing.RequestHash != task.RequestHash {
		return Task{}, false, ErrRequestHashConflict
	}
	return existing, false, nil
}

func (p *Postgres) GetByTaskID(ctx context.Context, taskID string) (Task, error) {
	row, err := p.client.GenerationTask.Query().
		Where(generationtask.IDEQ(taskID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return fromEnt(row), nil
}

func (p *Postgres) getByCallerRequest(ctx context.Context, caller, requestID string) (Task, error) {
	row, err := p.client.GenerationTask.Query().
		Where(
			generationtask.CallerEQ(caller),
			generationtask.RequestIDEQ(requestID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return fromEnt(row), nil
}

// MarkRunning 仅 PENDING→RUNNING；禁止 FAILED/SUCCEEDED 被后到写入复活。
func (p *Postgres) MarkRunning(ctx context.Context, taskID, providerName, providerTaskID string) error {
	n, err := p.client.GenerationTask.Update().
		Where(
			generationtask.IDEQ(taskID),
			generationtask.StateEQ(StatePending),
		).
		SetProvider(providerName).
		SetProviderTaskID(providerTaskID).
		SetState(StateRunning).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailed 仅允许 expectedState→FAILED；避免过期 PENDING 收口误杀已被 MarkRunning 的有效任务。
func (p *Postgres) MarkFailed(ctx context.Context, taskID, expectedState string, code int32, message, reason string) error {
	n, err := p.client.GenerationTask.Update().
		Where(
			generationtask.IDEQ(taskID),
			generationtask.StateEQ(expectedState),
		).
		SetState(StateFailed).
		SetErrorCode(code).
		SetErrorMessage(message).
		SetErrorReason(reason).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkSucceeded 仅 RUNNING→SUCCEEDED；禁止覆盖 FAILED。
func (p *Postgres) MarkSucceeded(ctx context.Context, taskID string) error {
	n, err := p.client.GenerationTask.Update().
		Where(
			generationtask.IDEQ(taskID),
			generationtask.StateEQ(StateRunning),
		).
		SetState(StateSucceeded).
		SetErrorCode(0).
		SetErrorMessage("").
		SetErrorReason("").
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// fromEnt 只做 Ent 实体到稳定 Store DTO 的边界映射，避免上层依赖生成代码字段形状。
func fromEnt(row *ent.GenerationTask) Task {
	return Task{
		TaskID:         row.ID,
		Caller:         row.Caller,
		RequestID:      row.RequestID,
		RequestHash:    row.RequestHash,
		Model:          row.Model,
		Provider:       row.Provider,
		ProviderTaskID: row.ProviderTaskID,
		State:          row.State,
		ErrorCode:      row.ErrorCode,
		ErrorMessage:   row.ErrorMessage,
		ErrorReason:    row.ErrorReason,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}
