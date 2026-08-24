package taskstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

// Postgres 访问 generation_task；不做启动 DDL。
type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool}
}

func OpenPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func (p *Postgres) InsertPending(ctx context.Context, task Task) (Task, bool, error) {
	const insertSQL = `
INSERT INTO generation_task (
	task_id, caller, request_id, request_hash, model, provider, provider_task_id, state,
	error_code, error_message, error_reason
) VALUES ($1,$2,$3,$4,$5,$6,'',$7,0,'','')
ON CONFLICT (caller, request_id) DO NOTHING
RETURNING task_id, caller, request_id, request_hash, model, provider, provider_task_id, state,
	error_code, error_message, error_reason, created_at, updated_at`

	stored, err := scanTask(p.pool.QueryRow(ctx, insertSQL,
		task.TaskID, task.Caller, task.RequestID, task.RequestHash, task.Model, task.Provider, StatePending,
	))
	if err == nil {
		return stored, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Task{}, false, err
	}
	existing, err := p.getByCallerRequest(ctx, task.Caller, task.RequestID)
	if err != nil {
		return Task{}, false, err
	}
	if existing.RequestHash != task.RequestHash {
		return Task{}, false, ErrRequestHashConflict
	}
	return existing, false, nil
}

func (p *Postgres) GetByTaskID(ctx context.Context, taskID string) (Task, error) {
	const q = `
SELECT task_id, caller, request_id, request_hash, model, provider, provider_task_id, state,
	error_code, error_message, error_reason, created_at, updated_at
FROM generation_task WHERE task_id = $1`
	task, err := scanTask(p.pool.QueryRow(ctx, q, taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

func (p *Postgres) getByCallerRequest(ctx context.Context, caller, requestID string) (Task, error) {
	const q = `
SELECT task_id, caller, request_id, request_hash, model, provider, provider_task_id, state,
	error_code, error_message, error_reason, created_at, updated_at
FROM generation_task WHERE caller = $1 AND request_id = $2`
	task, err := scanTask(p.pool.QueryRow(ctx, q, caller, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// MarkRunning 仅 PENDING→RUNNING；禁止 FAILED/SUCCEEDED 被后到写入复活。
func (p *Postgres) MarkRunning(ctx context.Context, taskID, providerName, providerTaskID string) error {
	tag, err := p.pool.Exec(ctx, `
UPDATE generation_task
SET provider = $2, provider_task_id = $3, state = $4, updated_at = CURRENT_TIMESTAMP
WHERE task_id = $1 AND state = $5`, taskID, providerName, providerTaskID, StateRunning, StatePending)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailed 仅允许 expectedState→FAILED；避免过期 PENDING 收口误杀已被 MarkRunning 的有效任务。
func (p *Postgres) MarkFailed(ctx context.Context, taskID, expectedState string, code int32, message, reason string) error {
	tag, err := p.pool.Exec(ctx, `
UPDATE generation_task
SET state = $2, error_code = $3, error_message = $4, error_reason = $5, updated_at = CURRENT_TIMESTAMP
WHERE task_id = $1 AND state = $6`, taskID, StateFailed, code, message, reason, expectedState)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkSucceeded 仅 RUNNING→SUCCEEDED；禁止覆盖 FAILED。
func (p *Postgres) MarkSucceeded(ctx context.Context, taskID string) error {
	tag, err := p.pool.Exec(ctx, `
UPDATE generation_task
SET state = $2, error_code = 0, error_message = '', error_reason = '', updated_at = CURRENT_TIMESTAMP
WHERE task_id = $1 AND state = $3`, taskID, StateSucceeded, StateRunning)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanTask(row scannable) (Task, error) {
	var task Task
	err := row.Scan(
		&task.TaskID, &task.Caller, &task.RequestID, &task.RequestHash, &task.Model, &task.Provider,
		&task.ProviderTaskID, &task.State, &task.ErrorCode, &task.ErrorMessage, &task.ErrorReason,
		&task.CreatedAt, &task.UpdatedAt,
	)
	return task, err
}
