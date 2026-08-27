package dbmigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wgdl666/wgModelHub/migrations"
)

const advisoryLockKey int64 = 0x57474d4f44454c

func Run(ctx context.Context, db *sql.DB) error {
	return runSQL(ctx, db, migrations.GenerationTaskSQL)
}

func runSQL(ctx context.Context, db *sql.DB, statement string) (returnErr error) {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	if strings.TrimSpace(statement) == "" {
		return fmt.Errorf("migration SQL is empty")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("release migration advisory lock: %w", err)
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return fmt.Errorf("execute migration: %w (rollback: %v)", err, rollbackErr)
		}
		return fmt.Errorf("execute migration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
