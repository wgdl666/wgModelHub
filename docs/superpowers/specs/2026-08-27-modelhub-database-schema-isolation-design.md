# ModelHub Database Schema Isolation Design

**Status:** Approved design

**Date:** 2026-08-27

## 1. Goal

Store all ModelHub relational state in the `modelhub` schema inside the shared
`wgdl` development database used by `wgRecommendService`: long-running
generation state in `modelhub.generation_task` and public API Key state in
`modelhub.modelhub_api_key`. Do not create database objects during application
startup or change the shared Recommend dev DSN.

## 2. Current state

- ModelHub AppConfig `modelhub / dev / config-dev` uses the exact same database
  DSN as `wgRecommendService / dev / config-dev`.
- The active database is the Singapore RDS instance `wg-dev-pg`, database
  `wgdl`.
- Neither the `modelhub` schema nor the legacy public-schema ModelHub tables
  currently exist.
- The configured database role has `CREATE` privilege on `wgdl`.
- Runtime persistence uses Ent-generated builders and predicates. Application
  startup never calls `Schema.Create` or otherwise executes DDL.

## 3. Chosen design

### 3.1 Explicit migration ownership

`migrations/001_generation_task.sql` remains the only migration permitted for
the internal AWS ModelHub release. It will execute these operations in order:

1. `CREATE SCHEMA IF NOT EXISTS modelhub`.
2. `CREATE TABLE IF NOT EXISTS modelhub.generation_task (...)` with the existing
   columns, primary key, defaults, timestamps, and `(caller, request_id)` unique
   constraint unchanged.

The existing migration runner continues to acquire its PostgreSQL advisory
lock and execute the complete embedded SQL inside one transaction. PostgreSQL
schema and table creation are transactional, so a failure rolls back both
operations before the advisory lock is released.

The server and migration binaries remain separate. The server does not invoke
the migration runner, and the migration is executed only after explicit human
approval. Migrations `002_modelhub_api_key.sql` and
`003_modelhub_api_key_expires_nullable.sql`, which target
`modelhub.modelhub_api_key`, remain forbidden for this release.

### 3.2 Runtime table qualification

Both Ent schemas declare their tables in PostgreSQL schema `modelhub`:

- `GenerationTask` resolves to `modelhub.generation_task`.
- `ModelhubAPIKey` resolves to `modelhub.modelhub_api_key`.

Ent code will be regenerated with the repository's existing
`go generate ./ent` command. Generated create, query, update, and delete specs
must carry the `modelhub` schema, so runtime access resolves explicitly to
`modelhub.generation_task` and `modelhub.modelhub_api_key` and does not depend
on the connection's `search_path`.

No handwritten business SQL or DSN `search_path` override will be introduced.
The AppConfig DSN therefore remains byte-for-byte equal to Recommend dev.

### 3.3 Test database compatibility

Production qualification must not weaken the existing SQLite-backed store
tests. Their in-memory SQLite connection will attach a second in-memory
database named `modelhub` before Ent's test-only schema creation runs. This
lets the generated qualified table name behave like the PostgreSQL schema
while keeping automatic DDL confined to tests.

Tests will assert all of the following:

- embedded migration SQL creates the `modelhub` schema;
- the table DDL names `modelhub.generation_task` and does not create
  `public.generation_task` or an unqualified production table;
- generated Ent metadata identifies the schema as `modelhub`;
- API Key migration definitions target `modelhub.modelhub_api_key` while
  remaining excluded from the AWS first release;
- existing task-store create, idempotency, lookup, and state-transition tests
  still pass against the attached SQLite database;
- application startup paths still contain no `Schema.Create` call.

## 4. Deployment sequence

1. Commit and push the source and generated Ent changes to `dev`.
2. Update the platform manifest to pin the new source commit, Dockerfile hash,
   image-contract hash, and `001` migration hash as required by the existing
   immutable build contract.
3. Let the automatic pipeline build and scan new production and migration
   images while the ECS service remains at zero desired tasks.
4. Run `-validate-config-only`; this loads AppConfig but neither connects to the
   database nor executes DDL.
5. At the explicit migration approval gate, run the fixed migration image once
   against Recommend dev's `wgdl` database.
6. Verify read-only that `modelhub.generation_task` exists, its owner and
   columns match `001`, and `public.generation_task` remains absent.
7. Approve the application release, start two private ECS tasks, and perform
   Cloud Map, gRPC health, persistence, and telemetry checks.

## 5. Failure and rollback behavior

- A migration error rolls back schema and table creation and prevents the
  application release.
- A failed application deployment rolls back to the previous immutable image;
  it does not drop `modelhub.generation_task`.
- The migration is idempotent because both schema and table use `IF NOT
  EXISTS`.
- There is no automatic down migration. Dropping the schema or table would be a
  separate destructive operation requiring a new explicit approval.
- Because the table does not yet exist, this change requires no data copy,
  rename, or compatibility view.

## 6. Acceptance criteria

- Every ModelHub relational table is schema-qualified in `modelhub`; runtime
  task-store SQL resolves to `modelhub.generation_task` and API Key SQL resolves
  to `modelhub.modelhub_api_key` through generated Ent code.
- The only ModelHub table created by the approved AWS migration is
  `modelhub.generation_task`; migrations `002/003` for
  `modelhub.modelhub_api_key` remain excluded from the first release.
- Legacy public-schema ModelHub tables remain absent.
- ModelHub and Recommend dev retain an identical AppConfig database DSN.
- Application startup performs no schema or table creation.
- Only migration `001` is embedded in and executable by the approved migration
  image.
