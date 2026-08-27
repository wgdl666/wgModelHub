# ModelHub Database Schema Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move ModelHub's generation task persistence from the shared default namespace to the explicitly isolated `modelhub.generation_task` table.

**Architecture:** The explicit `001` migration creates the `modelhub` PostgreSQL schema and its generation task table in one advisory-locked transaction. Ent's schema annotation and regenerated builders qualify every runtime operation with `modelhub`, while the test-only SQLite client attaches a `modelhub` database and directs Ent's test migration there. The shared Recommend dev DSN remains unchanged and application startup continues to perform no DDL.

**Tech Stack:** Go 1.26, Ent 0.14.5, pgx 5.7.6, PostgreSQL 18, sqlmock, modernc SQLite, AWS AppConfig, ECS Fargate, TypeScript 5.9, Vitest 4, AWS CDK 2.263.0

## Global Constraints

- Work in `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev` for ModelHub and `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub` for infrastructure.
- Use `apply_patch` for hand-authored file changes; generated Ent output must come only from `go generate ./ent`.
- Do not print, commit, or log AppConfig content, database DSNs, credentials, prompts, media, or response bodies.
- Keep ModelHub and Recommend dev's AppConfig database DSN byte-for-byte identical.
- Application startup must not call `Schema.Create`, execute migration SQL, create a schema, or create a table.
- Only `migrations/001_generation_task.sql` may be embedded in or executed by the AWS migration image; migrations `002` and `003` remain forbidden.
- The ECS service remains at desired count zero and the pipeline remains behind manual application approval until the new `001` migration receives a separate explicit approval.
- No public load balancer, public IP, public DNS record, HTTP listener, API key resource, or direct service-to-service bypass is added.

---

### Task 1: Qualify the explicit `001` migration

**Files:**
- Create: `migrations/embed_test.go`
- Modify: `migrations/001_generation_task.sql`
- Modify: `internal/dbmigration/migrate_test.go`

**Interfaces:**
- Consumes: `migrations.GenerationTaskSQL string` from `migrations/embed.go` and `dbmigration.runSQL(context.Context, *sql.DB, string) error`.
- Produces: an idempotent embedded migration that creates `modelhub` and `modelhub.generation_task` in the runner's existing transaction.

- [ ] **Step 1: Write the failing migration target contract**

Create `migrations/embed_test.go`:

```go
package migrations

import (
	"strings"
	"testing"
)

func TestGenerationTaskSQLTargetsModelhubSchema(t *testing.T) {
	for _, fragment := range []string{
		"CREATE SCHEMA IF NOT EXISTS modelhub",
		"CREATE TABLE IF NOT EXISTS modelhub.generation_task",
	} {
		if !strings.Contains(GenerationTaskSQL, fragment) {
			t.Fatalf("001 migration missing %q", fragment)
		}
	}
	if strings.Contains(GenerationTaskSQL, "CREATE TABLE IF NOT EXISTS generation_task") {
		t.Fatal("001 migration must not create an unqualified generation_task table")
	}
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./migrations -run TestGenerationTaskSQLTargetsModelhubSchema -count=1
```

Expected: FAIL because the current migration has neither the schema creation nor the qualified table name.

- [ ] **Step 3: Implement the minimal qualified migration**

Change the beginning of `migrations/001_generation_task.sql` to:

```sql
-- 视频长任务最小技术表：只存可跨 Pod 查询的归因与上游定位信息。
-- 不保存 prompt、媒体正文或最终视频；不在进程启动时自动执行本文件。
CREATE SCHEMA IF NOT EXISTS modelhub;

CREATE TABLE IF NOT EXISTS modelhub.generation_task (
```

Keep every existing column, default, primary key, timestamp, and unique constraint unchanged. Update `generationTaskDDL` in `internal/dbmigration/migrate_test.go` so the sqlmock runner test executes a two-statement schema-plus-table string:

```go
const generationTaskDDL = `CREATE SCHEMA IF NOT EXISTS modelhub;
CREATE TABLE IF NOT EXISTS modelhub.generation_task (
    task_id text PRIMARY KEY
);`
```

- [ ] **Step 4: Verify GREEN and transaction behavior**

Run:

```bash
go test ./migrations ./internal/dbmigration -count=1
```

Expected: PASS, including commit, rollback, advisory unlock, and qualified migration target tests.

- [ ] **Step 5: Commit the migration contract**

```bash
git add migrations/embed_test.go migrations/001_generation_task.sql internal/dbmigration/migrate_test.go
git commit -m "feat(db): isolate generation tasks in modelhub schema"
```

---

### Task 2: Generate schema-qualified Ent runtime operations

**Files:**
- Create: `ent/schema/generation_task_test.go`
- Create: `ent/schema_config_test.go`
- Modify: `ent/schema/generation_task.go`
- Generate: `ent/config.go`
- Generate: `ent/internal/schema.go`
- Regenerate: `ent/client.go`
- Regenerate: `ent/generationtask_create.go`
- Regenerate: `ent/generationtask_delete.go`
- Regenerate: `ent/generationtask_query.go`
- Regenerate: `ent/generationtask_update.go`
- Regenerate: `ent/modelhubapikey_create.go`
- Regenerate: `ent/modelhubapikey_delete.go`
- Regenerate: `ent/modelhubapikey_query.go`
- Regenerate: `ent/modelhubapikey_update.go`

**Interfaces:**
- Consumes: `GenerationTask.Annotations() []schema.Annotation` and Ent's `entsql.Annotation.Schema` multi-schema generator support.
- Produces: `ent.DefaultSchemaConfig.GenerationTask == "modelhub"`; all generated GenerationTask create/query/update/delete specs use that schema.

- [ ] **Step 1: Write the failing source annotation test**

Create `ent/schema/generation_task_test.go`:

```go
package schema

import (
	"testing"

	"entgo.io/ent/dialect/entsql"
)

func TestGenerationTaskUsesModelhubSchema(t *testing.T) {
	annotations := (GenerationTask{}).Annotations()
	if len(annotations) != 1 {
		t.Fatalf("annotations=%d, want 1", len(annotations))
	}
	annotation, ok := annotations[0].(entsql.Annotation)
	if !ok {
		t.Fatalf("annotation type=%T, want entsql.Annotation", annotations[0])
	}
	if annotation.Table != "generation_task" || annotation.Schema != "modelhub" {
		t.Fatalf("table=%q schema=%q", annotation.Table, annotation.Schema)
	}
}
```

- [ ] **Step 2: Verify the annotation test fails**

Run:

```bash
go test ./ent/schema -run TestGenerationTaskUsesModelhubSchema -count=1
```

Expected: FAIL with `schema=""`.

- [ ] **Step 3: Add the Ent schema annotation**

Change `GenerationTask.Annotations` in `ent/schema/generation_task.go` to:

```go
func (GenerationTask) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{
		Schema: "modelhub",
		Table:  "generation_task",
	}}
}
```

Update the adjacent comment to name `modelhub.generation_task` and retain the prohibition on runtime `Schema.Create`.

- [ ] **Step 4: Add the generated schema contract before regeneration**

Create `ent/schema_config_test.go`:

```go
package ent_test

import (
	"testing"

	"github.com/wgdl666/wgModelHub/ent"
)

func TestDefaultSchemaConfigQualifiesOnlyGenerationTask(t *testing.T) {
	if got := ent.DefaultSchemaConfig.GenerationTask; got != "modelhub" {
		t.Fatalf("GenerationTask schema=%q, want modelhub", got)
	}
	if got := ent.DefaultSchemaConfig.ModelhubAPIKey; got != "" {
		t.Fatalf("ModelhubAPIKey schema=%q, want empty", got)
	}
}
```

Run:

```bash
go test ./ent -run TestDefaultSchemaConfigQualifiesOnlyGenerationTask -count=1
```

Expected: compilation FAIL because the checked-in generated client does not yet expose `DefaultSchemaConfig`.

- [ ] **Step 5: Regenerate Ent code**

```bash
go generate ./ent
gofmt -w ent/schema/generation_task.go ent/schema/generation_task_test.go ent/schema_config_test.go
git diff --check
```

Review every generated diff. It may add schema configuration plumbing to both generated entity builders, but `DefaultSchemaConfig.ModelhubAPIKey` must stay empty and no generated file may call schema migration at application startup.

- [ ] **Step 6: Verify generated runtime qualification**

Run:

```bash
go test ./ent ./ent/schema -run 'TestDefaultSchemaConfigQualifiesOnlyGenerationTask|TestGenerationTaskUsesModelhubSchema' -count=1
rg -n 'Schema = .*GenerationTask|t1.Schema\(' ent/generationtask_create.go ent/generationtask_query.go ent/generationtask_update.go ent/generationtask_delete.go
```

Expected: tests PASS and generated CRUD specs/selector contain schema configuration assignments.

- [ ] **Step 7: Commit source and generated Ent output**

```bash
git add ent
git commit -m "feat(db): qualify generation task Ent queries"
```

---

### Task 3: Keep the SQLite task-store suite representative

**Files:**
- Modify: `internal/taskstore/store_test.go`

**Interfaces:**
- Consumes: the generated default `modelhub` schema for GenerationTask and Ent's test-only migration option `schema.WithSchemaName(string)`.
- Produces: an attached in-memory SQLite database named `modelhub` containing the test-only Ent tables, so qualified runtime operations remain covered without production DDL.

- [ ] **Step 1: Run the existing store suite and verify RED**

```bash
go test ./internal/taskstore -count=1
```

Expected: FAIL because generated queries target `modelhub.generation_task` while the current SQLite setup has no attached `modelhub` database.

- [ ] **Step 2: Attach the test schema and direct test migration to it**

Add the migration schema import:

```go
entschema "entgo.io/ent/dialect/sql/schema"
```

In `openTestStore`, immediately after opening `db`, attach the test-only schema:

```go
if _, err := db.Exec(`ATTACH DATABASE ':memory:' AS modelhub`); err != nil {
	t.Fatalf("attach modelhub SQLite database: %v", err)
}
```

Construct the Ent test client with both existing driver injection and the explicit test migration schema:

```go
client := enttest.NewClient(
	t,
	enttest.WithOptions(ent.Driver(drv)),
	enttest.WithMigrateOptions(entschema.WithSchemaName("modelhub")),
)
```

Automatic DDL remains confined to this SQLite test helper. Do not add `Schema.Create` to production code.

- [ ] **Step 3: Verify GREEN**

```bash
gofmt -w internal/taskstore/store_test.go
go test ./internal/taskstore -count=1
```

Expected: all create, idempotency, lookup, conditional transition, and terminal-state tests PASS through `modelhub.generation_task`.

- [ ] **Step 4: Prove startup still has no automatic DDL**

```bash
if rg -n 'Schema\.Create|CREATE SCHEMA|CREATE TABLE' cmd/server internal --glob '*.go'; then
  echo 'unexpected production startup DDL' >&2
  exit 1
fi
```

Expected: no matches and exit code zero.

- [ ] **Step 5: Commit the test harness adaptation**

```bash
git add internal/taskstore/store_test.go
git commit -m "test(db): exercise ModelHub schema in task store"
```

---

### Task 4: Update service contracts and verify ModelHub

**Files:**
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-08-27-wgmodelhub-aws-dev-deployment-design.md`
- Modify: `docs/superpowers/specs/2026-08-27-modelhub-database-schema-isolation-design.md` only if implementation evidence requires a wording correction

**Interfaces:**
- Consumes: the qualified migration and generated Ent runtime from Tasks 1-3.
- Produces: repository guidance and deployment acceptance criteria that consistently name `modelhub.generation_task`.

- [ ] **Step 1: Write a failing repository contract check**

Run before editing documentation:

```bash
if rg -n '`generation_task`| generation_task ' AGENTS.md README.md docs/superpowers/specs/2026-08-27-wgmodelhub-aws-dev-deployment-design.md; then
  exit 1
fi
```

Expected: exit code 1 because current documentation still names the unqualified table.

- [ ] **Step 2: Qualify the documentation**

Use `modelhub.generation_task` wherever the documents describe migration `001`, runtime persistence, post-migration verification, or rollback. Preserve all statements that startup performs no DDL and that migrations `002/003` are excluded.

- [ ] **Step 3: Run complete ModelHub verification**

```bash
gofmt -w internal/dbmigration/migrate_test.go internal/taskstore/store_test.go migrations/embed_test.go ent/schema/generation_task.go ent/schema/generation_task_test.go ent/schema_config_test.go
go test ./... -count=1
go vet ./...
bash scripts/platform/verify_platform_image_paths_test.sh
git diff --check
```

Expected: all tests PASS, vet emits no diagnostics, the shell contract test passes, and no whitespace errors exist.

- [ ] **Step 4: Review source scope and commit documentation**

```bash
git status --short
git diff --stat
git diff -- AGENTS.md README.md docs/superpowers/specs
git add AGENTS.md README.md docs/superpowers/specs/2026-08-27-wgmodelhub-aws-dev-deployment-design.md docs/superpowers/specs/2026-08-27-modelhub-database-schema-isolation-design.md
git commit -m "docs: qualify ModelHub generation task storage"
```

If the isolation design required no wording correction, omit that unchanged file from `git add`.

- [ ] **Step 5: Push the reviewed ModelHub commits**

```bash
git status --short
git push origin dev
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/dev)"
```

Expected: clean worktree and local `dev` equals `origin/dev`.

---

### Task 5: Pin the new source and schema evidence in platform infrastructure

**Files:**
- Modify: `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub/config/services/modelhub.yaml`
- Modify: `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub/test/services/modelhub.test.ts`
- Modify: `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub/docs/runbooks/modelhub-first-release.md`

**Interfaces:**
- Consumes: the pushed ModelHub `dev` SHA and SHA-256 of its qualified `migrations/001_generation_task.sql`.
- Produces: a platform manifest pinned to the reviewed source and a runbook that accepts only `modelhub.generation_task` while requiring `public.generation_task` to remain absent.

- [ ] **Step 1: Capture immutable source evidence**

From the infra worktree:

```bash
modelhub_tree=/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev
modelhub_sha="$(git -C "$modelhub_tree" rev-parse HEAD)"
migration_sha="$(shasum -a 256 "$modelhub_tree/migrations/001_generation_task.sql" | awk '{print $1}')"
test "${#modelhub_sha}" -eq 40
test "${#migration_sha}" -eq 64
```

- [ ] **Step 2: Write the failing infra expectations**

In `test/services/modelhub.test.ts`, set `SOURCE_COMMIT` to the captured `modelhub_sha` and add a runbook contract test that reads `docs/runbooks/modelhub-first-release.md` and requires all of:

```ts
expect(runbook).toContain("modelhub.generation_task");
expect(runbook).toContain("public.generation_task");
expect(runbook).toContain("CREATE SCHEMA IF NOT EXISTS modelhub");
```

Run:

```bash
npx vitest run test/services/modelhub.test.ts
```

Expected: FAIL because the manifest and runbook still carry the old source/table evidence.

- [ ] **Step 3: Update the manifest and first-release runbook**

Set `config/services/modelhub.yaml` `source.commit` to the captured `modelhub_sha`. In the runbook:

- replace the immutable source commit with `modelhub_sha`;
- replace the `001` SHA-256 with `migration_sha`;
- require migration `001` to create `modelhub` and `modelhub.generation_task`;
- require read-only post-migration verification that `modelhub.generation_task` exists and `public.generation_task` is absent;
- retain `releaseGate: manual-approval`, the separate migration approval, desired count zero, and the ban on `002/003`.

- [ ] **Step 4: Verify the complete infra repository**

```bash
npx vitest run test/services/modelhub.test.ts
npm test
npm run build
npm run schemas
npm run validate
npx cdk synth wg-dev-modelhub-service --context environment=wg-dev >/dev/null
git diff --check
```

Expected: focused and full tests PASS, TypeScript builds, schemas and manifests validate, and CDK synthesis succeeds.

- [ ] **Step 5: Commit and push infrastructure evidence**

```bash
git add config/services/modelhub.yaml test/services/modelhub.test.ts docs/runbooks/modelhub-first-release.md
git commit -m "chore(modelhub): pin isolated schema release"
git push origin feat/modelhub-dev
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/feat/modelhub-dev)"
```

Expected: clean infra worktree and remote branch pinned to the reviewed ModelHub commit.

---

### Task 6: Build the corrected images and stop at the migration gate

**Files:**
- No source files; AWS state changes only within the existing ModelHub dev pipeline, ECR repository, AppConfig identity, ECS cluster, and CloudFormation stack.

**Interfaces:**
- Consumes: pushed source/infra commits, AppConfig version 3, existing private subnets and migration security group, immutable ECR repositories, and the existing manual release gate.
- Produces: scanned production and migration image digests plus a successful `-validate-config-only` task; it does not produce database objects or running application replicas.

- [ ] **Step 1: Reconfirm the release safety boundary**

```bash
aws sts get-caller-identity --profile wgdl-new --query Account --output text
aws ecs describe-services --cluster wg-dev --services modelhub-service --region ap-southeast-1 --profile wgdl-new --query 'services[0].[desiredCount,runningCount,pendingCount]' --output text
aws appconfig get-deployment --application-id u8e7hop --environment-id mk0jmep --deployment-number 3 --region us-east-2 --profile wgdl-new --query '[State,ConfigurationVersion]' --output text
```

Expected: account `054043816891`, ECS counts `0 0 0`, AppConfig state `COMPLETE`, version `3`.

- [ ] **Step 2: Stop the stale pre-schema pipeline execution**

Inspect the current pipeline state. If execution `e185ca75-5777-4668-826e-0eb11baa356f` is still waiting at `ApproveApplicationRelease`, stop only that exact execution with reason `Superseded by modelhub schema-qualified release`. Do not approve it.

- [ ] **Step 3: Deploy only the reviewed manifest update**

Synthesize `wg-dev-modelhub-service`, compare the generated template to the deployed stack, and create a named CloudFormation change set using the existing artifact bucket because the local CDK client currently has an AWS SDK deserialization defect. Review the change set before execution. It may update pipeline/source metadata but must not raise ECS desired count, remove the manual gate, add public networking, or execute a task.

- [ ] **Step 4: Start and monitor the corrected pipeline**

Start `wg-dev-modelhub` once, record the execution ID, and wait for Source, Test, and Build to succeed. Confirm Release is waiting at `ApproveApplicationRelease`; do not approve it.

Resolve immutable production and migration digests from tags formed with the exact pushed ModelHub SHA. Require both ECR scans to be `COMPLETE`, zero CRITICAL findings, and no unreviewed fixable HIGH findings.

- [ ] **Step 5: Register and run only the configuration validator task**

Register a new `modelhub-dev-migration-once` revision with the new migration digest and the existing AppConfig Agent digest. Run it in the three existing private subnets, migration security group, and `assignPublicIp=DISABLED`, overriding only the migration container command to:

```json
["-validate-config-only"]
```

Wait for the task to stop. Require the AppConfig Agent to have been healthy and both containers to exit `0`.

- [ ] **Step 6: Reconfirm that no migration or release occurred**

Use read-only checks to require:

- `modelhub` schema absent;
- `modelhub.generation_task` absent;
- `public.generation_task` absent;
- ModelHub ECS desired/running/pending counts remain `0/0/0`;
- the corrected pipeline remains at manual application approval.

Present the new source SHA, migration SHA, image digests, scan summary, task definition revision, and config-validation task ARN without exposing credentials. Request a separate explicit approval before running migration `001`.
