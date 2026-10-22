package ent

import (
	"context"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
)

// TryPrefixCacheLock uses the same transaction/connection as registry writes.
// A missing registry row can still be locked. Commit/rollback releases the lock.
func (tx *Tx) TryPrefixCacheLock(ctx context.Context, key int64) (bool, error) {
	query, args := sql.Dialect(dialect.Postgres).SelectExpr(sql.ExprFunc(func(b *sql.Builder) {
		b.WriteString("pg_try_advisory_xact_lock(").Arg(key).WriteByte(')')
	})).Query()
	var rows sql.Rows
	if err := tx.config.driver.Query(ctx, query, args, &rows); err != nil {
		return false, err
	}
	defer rows.Close()
	var acquired bool
	if rows.Next() {
		if err := rows.Scan(&acquired); err != nil {
			return false, err
		}
	}
	return acquired, rows.Err()
}
