package dbmigration

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const generationTaskDDL = `CREATE TABLE IF NOT EXISTS generation_task (
    task_id text PRIMARY KEY
);`

func TestRunExecutesMigrationInsideAdvisoryLockAndTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_lock($1)")).
		WithArgs(advisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(generationTaskDDL)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(advisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := runSQL(context.Background(), db, generationTaskDDL); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunRollsBackAndUnlocksWhenMigrationFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	databaseErr := errors.New("database rejected DDL")
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_lock($1)")).
		WithArgs(advisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(generationTaskDDL)).WillReturnError(databaseErr)
	mock.ExpectRollback()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(advisoryLockKey).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = runSQL(context.Background(), db, generationTaskDDL)
	if !errors.Is(err, databaseErr) {
		t.Fatalf("error=%v, want wrapped database error", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
