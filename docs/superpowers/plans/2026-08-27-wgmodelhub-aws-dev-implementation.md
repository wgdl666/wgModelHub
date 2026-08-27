# wgModelHub AWS Dev Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an AppConfig-selected runtime to `wgModelHub`, deliver it through an automatic `dev` pipeline, and deploy two internal-only gRPC tasks to AWS Singapore.

**Architecture:** The application chooses a strict runtime loader with `WG_CONFIG_SOURCE`, preserving the current Nacos loader while adding an AppConfig Agent loader. `wgPlatformInfra` imports the existing Ohio AppConfig resources and provisions a private Fargate service, Cloud Map discovery, an immutable ECR/CodePipeline flow, and separately approved migration identities.

**Tech Stack:** Go 1.26, gRPC, PostgreSQL/pgx, AWS AppConfig Agent, ECS Fargate, Cloud Map, ECR, CodePipeline/CodeBuild, AWS CDK 2.263.0, TypeScript 5.9, Vitest 4.

## Global Constraints

- Work only on `wgModelHub/dev`, created from `main` commit `5cf6d5b158b7f164e503928e20b1983e19b19dfb` or a later fast-forwarded `main` approved before implementation.
- Keep the existing Nacos default and listener behavior when `WG_CONFIG_SOURCE` is unset or `nacos`.
- AWS runtime configuration must come only from AppConfig `us-east-2 / modelhub(u8e7hop) / dev(mk0jmep) / config-dev(u1tlyq1)`.
- AWS exposes only internal gRPC `50053`; do not map `50054` or create a public load balancer, DNS record, certificate, or API Key tooling.
- Use `modelhub.internal.dev:50053`, two tasks, `1024` CPU units, `2048` MiB, private subnets, no public IP, and caller security group `sg-09930b896aeeb910f`.
- Keep PostgreSQL DDL out of server startup. Only the explicit migration command may execute `migrations/001_generation_task.sql`.
- Do not execute migrations `002` or `003` in this internal-only rollout.
- Do not print or commit AppConfig content, DSNs, provider credentials, prompts, media, or response bodies.
- Preserve user-owned changes in `/Users/bruce/workspaces/wgdl_aws/wgPlatformInfra`; use a separate worktree based on its current local `main` commit.
- Treat the three locally FFmpeg-dependent Gemini video failures as an approved baseline issue; no additional package may fail.
- Use Logfire project `server` for AWS runtime verification.
- Review every staged diff before commit. Ask the user before pushing either repository.

---

### Task 1: Add the fail-closed AppConfig Agent loader

**Files:**
- Create: `config/appconfig_loader.go`
- Create: `config/appconfig_loader_test.go`
- Modify: `config/config.go`

**Interfaces:**
- Consumes: `ParseAndValidateYAML(content string) (Config, error)`.
- Produces: `NewAppConfigLoaderFromEnv() (*AppConfigLoader, error)` and `(*AppConfigLoader).Load(context.Context) (Config, string, error)`.

- [ ] **Step 1: Write failing constructor and response-boundary tests**

Add table tests that set exactly:

```go
t.Setenv("APP_NAME", "modelhub")
t.Setenv("ENV", "dev")
t.Setenv("SERVICE_NAME", "config-dev")
t.Setenv("REGION", "us-east-2")
t.Setenv("AWS_APPCONFIG_AGENT_ENDPOINT", server.URL)
```

The success handler must assert the request path equals `/applications/modelhub/environments/dev/configurations/config-dev` and return `mustYAML(t, validConfig())`. Add independent failing cases for a non-loopback endpoint, redirect, HTTP 500, empty body, a body larger than `256 << 10`, invalid YAML, and a mismatched application/environment/profile/region identity.

- [ ] **Step 2: Run the focused tests and capture the expected failure**

```bash
go test ./config -run 'TestNewAppConfigLoader|TestAppConfigLoader' -count=1
```

Expected: compile failure because `NewAppConfigLoaderFromEnv` does not exist.

- [ ] **Step 3: Implement the loader with fixed identity and bounded HTTP behavior**

```go
const (
	defaultAppConfigAgentEndpoint = "http://127.0.0.1:2772"
	appConfigApplication          = "modelhub"
	appConfigEnvironment          = "dev"
	appConfigProfile              = "config-dev"
	appConfigRegion               = "us-east-2"
	maxAppConfigBytes             = 256 << 10
)

type AppConfigLoader struct {
	client *http.Client
	url    string
}
```

`NewAppConfigLoaderFromEnv` must reject any identity mismatch, parse the endpoint, require `http`, require a loopback IP or `localhost`, forbid userinfo/query/fragment, and configure a three-second timeout plus a redirect policy that returns `http.ErrUseLastResponse`. `Load` must issue a context-bound GET, require status 200, read through `io.LimitReader(maxAppConfigBytes+1)`, reject empty/oversize content, call `ParseAndValidateYAML`, and return the validated config plus the raw content for the existing live-config boundary. Error text may name the stage and field but must never contain the response body.

- [ ] **Step 4: Run loader and existing config tests**

```bash
go test ./config -run 'TestNewAppConfigLoader|TestAppConfigLoader|TestValidate|TestLiveConfig' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the AppConfig loader**

```bash
git add config/appconfig_loader.go config/appconfig_loader_test.go config/config.go
git diff --cached --check
git commit -m "feat(config): load AWS AppConfig through local agent"
```

---

### Task 2: Select runtime configuration without changing Nacos behavior

**Files:**
- Create: `config/runtime_loader.go`
- Create: `config/runtime_loader_test.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: `NewNacosConfigLoader(Bootstrap)` and `NewAppConfigLoaderFromEnv()`.
- Produces: `RuntimeLoader` and `NewRuntimeLoaderFromEnv() (RuntimeLoader, error)`.

- [ ] **Step 1: Write failing source-selection tests**

```go
type RuntimeLoader interface {
	Load(context.Context) (Config, string, error)
	Listen(func(dataID, group, content string)) error
	Close()
}
```

Test that unset and `nacos` select the Nacos loader using a temporary bootstrap file injected through a package test seam; `appconfig` selects `*AppConfigLoader`; and `file` or any unknown value returns `unsupported WG_CONFIG_SOURCE`. Test that `AppConfigLoader.Listen` is a no-op and `Close` is safe.

- [ ] **Step 2: Run the focused test and verify failure**

```bash
go test ./config -run 'TestRuntimeLoader|TestAppConfigListen' -count=1
```

Expected: compile failure because `RuntimeLoader` and `NewRuntimeLoaderFromEnv` are not implemented.

- [ ] **Step 3: Implement the runtime loader factory**

Use `strings.ToLower(strings.TrimSpace(os.Getenv("WG_CONFIG_SOURCE")))`; map `""` and `"nacos"` to the current bootstrap/Nacos constructor and map only `"appconfig"` to the Agent loader. Add no-op `Listen` and `Close` methods on `AppConfigLoader`. Keep `BootstrapFilePath` as the production Nacos path and expose only an unexported test seam for overriding it.

- [ ] **Step 4: Rewire server startup**

```go
configLoader, err := config.NewRuntimeLoaderFromEnv()
if err != nil {
	fatal("runtime_config_loader_init_failed", err)
}
defer configLoader.Close()

runtimeConfig, _, err := configLoader.Load(ctx)
if err != nil {
	fatal("runtime_config_load_failed", err)
}
```

Keep `ApplyListenPortOverridesFromEnv`, `LiveConfig`, and `Listen`. AppConfig makes `Listen` a no-op; Nacos remains unchanged.

- [ ] **Step 5: Run config and server package tests**

```bash
go test ./config ./cmd/server -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit runtime selection**

```bash
git add config/runtime_loader.go config/runtime_loader_test.go cmd/server/main.go
git diff --cached --check
git commit -m "feat(config): select Nacos or AppConfig runtime"
```

---

### Task 3: Add an explicit migration command and container health probe

**Files:**
- Create: `migrations/embed.go`
- Create: `internal/dbmigration/migrate.go`
- Create: `internal/dbmigration/migrate_test.go`
- Create: `cmd/migrate/main.go`
- Create: `cmd/healthcheck/main.go`
- Create: `cmd/healthcheck/main_test.go`
- Modify: `Dockerfile`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `RuntimeLoader`, AppConfig `database.dsn`, the standard gRPC Health service, and `migrations/001_generation_task.sql`.
- Produces: `dbmigration.Run(context.Context, *sql.DB) error`, `/usr/local/bin/wg-model-hub-migrate`, and `/usr/local/bin/wg-model-hub-healthcheck`.

- [ ] **Step 1: Write failing SQL execution tests**

Add `github.com/DATA-DOG/go-sqlmock` as a test dependency. Expect one connection to execute `SELECT pg_advisory_lock($1)`, begin, execute the embedded `CREATE TABLE IF NOT EXISTS generation_task` SQL, commit, and execute `SELECT pg_advisory_unlock($1)`. Add rollback and unlock expectations for a DDL error.

- [ ] **Step 2: Verify migration tests fail before implementation**

```bash
go test ./internal/dbmigration -count=1
```

Expected: compile failure because `dbmigration.Run` does not exist.

- [ ] **Step 3: Implement embedded, locked, transactional migration execution**

```go
package migrations

import _ "embed"

//go:embed 001_generation_task.sql
var GenerationTaskSQL string
```

`dbmigration.Run` must obtain one `*sql.Conn`, acquire a fixed signed 64-bit advisory-lock key, begin a transaction, execute `migrations.GenerationTaskSQL`, commit, and unlock in a defer. It must not enumerate or execute `002` or `003`.

- [ ] **Step 4: Implement the explicit migration command**

Create a signal-aware context, obtain the selected runtime loader, load and validate configuration, open `pgx` through `database/sql`, ping, call `dbmigration.Run`, and exit nonzero on error. Do not start telemetry, providers, gRPC, or schema auto-migration.

- [ ] **Step 5: Write and implement the gRPC health probe**

Test argument parsing and a local in-process health server. The command accepts `-address` and `-timeout`, dials with insecure transport credentials, calls `grpc.health.v1.Health/Check`, and succeeds only for `SERVING`.

- [ ] **Step 6: Update Docker targets**

Compile server, migrate, and healthcheck binaries. The production stage copies server plus healthcheck and keeps `ENTRYPOINT ["/usr/local/bin/wg-model-hub"]`. Add a separate final target:

```dockerfile
FROM runtime AS migration
COPY --from=builder /out/wg-model-hub-migrate /usr/local/bin/wg-model-hub-migrate
ENTRYPOINT ["/usr/local/bin/wg-model-hub-migrate"]
```

The runtime remains `10001:10001`, exposes only `50053`, and contains no standalone SQL files.

- [ ] **Step 7: Run focused tests and build all binaries**

```bash
go test ./internal/dbmigration ./cmd/healthcheck -count=1
go build ./cmd/server ./cmd/migrate ./cmd/healthcheck
```

Expected: PASS and successful builds.

- [ ] **Step 8: Commit migration and probe**

```bash
git add migrations/embed.go internal/dbmigration cmd/migrate cmd/healthcheck Dockerfile go.mod go.sum
git diff --cached --check
git commit -m "feat(deploy): add explicit migration and gRPC health tools"
```

---

### Task 4: Define AppConfig and container contracts

**Files:**
- Create: `schema/appconfig.schema.json`
- Create: `schema/appconfig_schema_test.go`
- Create: `scripts/platform/verify_platform_image.sh`
- Create: `.wg-platform/service.json`
- Modify: `.dockerignore`
- Modify: `README.md`

**Interfaces:**
- Consumes: YAML fields in `config.Config` and the production Docker image.
- Produces: a draft-04 AppConfig validator and `modelhub-shell` image contract.

- [ ] **Step 1: Write failing schema contract tests**

Parse `schema/appconfig.schema.json`; require draft-04, `id == "https://wgdl.tech/schemas/modelhub/appconfig.schema.json"`, no `$id`, required top-level `database`, `providers`, and `logfire`, and provider objects requiring `models` plus exactly one concrete provider kind. Require credential fields as strings while ensuring the schema contains no credential values.

- [ ] **Step 2: Verify schema tests fail**

```bash
go test ./schema -count=1
```

Expected: failure because the schema file is absent.

- [ ] **Step 3: Add the complete draft-04 schema**

Use `additionalProperties: false` for root and nested objects. Require a non-empty DSN, Logfire token, at least one provider, non-empty model IDs, and exactly one of `gemini`, `vertexai`, `ark`, `openai`, `ltx`, `dashscope_video`, `ominilink_video`, `gemini_video`, or `ark_video`. `model_routes` maps non-empty model IDs to non-empty provider names. Positive application durations/poll intervals use exclusive minimum zero.

- [ ] **Step 4: Add platform metadata and image verification**

`.wg-platform/service.json` identifies `modelhub`, `dev`, `linux/amd64`, `10001:10001`, port `50053`, AppConfig restart mode, private exposure, and caller SG `sg-09930b896aeeb910f`. The shell verifier checks exact user, architecture, entrypoint, exposed ports, source label, complete exported filesystem, absence of local config/bootstrap/source/SQL/private keys, and high-confidence credentials; it always cleans its temporary directory.

- [ ] **Step 5: Update documentation**

Document `WG_CONFIG_SOURCE`, exact AppConfig identity variables, `modelhub.internal.dev:50053`, internal-only scope, explicit migration, and blank `proxy_url` direct access. Preserve Aliyun/Nacos instructions.

- [ ] **Step 6: Run contract checks**

```bash
go test ./schema -count=1
bash -n scripts/platform/verify_platform_image.sh
```

Expected: PASS.

- [ ] **Step 7: Commit contracts**

```bash
git add schema scripts/platform .wg-platform .dockerignore README.md
git diff --cached --check
git commit -m "feat(deploy): define ModelHub platform contracts"
```

---

### Task 5: Generalize managed infrastructure for internal gRPC

**Files (`wgPlatformInfra` isolated worktree):**
- Modify: `lib/config/schema.ts`
- Modify: `lib/managed-internal-service-stack.ts`
- Modify: `lib/constructs/managed-release.ts`
- Modify: `scripts/release-managed.sh`
- Modify: `buildspec/build.yml`
- Modify: `config/services/sibyl.yaml`
- Modify: `test/fixtures/services/valid-managed.yaml`
- Modify: `test/stacks/managed-internal-service-stack.test.ts`
- Modify: `test/security/build-release-contracts.test.ts`

**Interfaces:**
- Consumes: current Sibyl HTTP behavior.
- Produces: generic container user/environment, `http|grpc` readiness, optional repository seed, and `modelhub-shell` support.

- [ ] **Step 1: Create and verify the infrastructure worktree**

Create branch `feat/modelhub-dev` from the current local `wgPlatformInfra/main` commit under `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub`. Run `npm ci`, `npm test`, and `npm run build`; do not touch the original dirty checkout.

- [ ] **Step 2: Write failing generic-runtime schema tests**

```yaml
runtime:
  containerUser: "10001:10001"
  environment:
    WG_CONFIG_SOURCE: appconfig
    WG_SERVER_GRPC_PORT: "50053"
  protocol: grpc
  port: 50053
  healthService: ""
```

HTTP runtimes must require health/readiness paths; gRPC runtimes reject HTTP-only fields; public IP remains false; optional `configuration.seedConfigPath` stays below `config/appconfig/`.

- [ ] **Step 3: Extend schema and preserve Sibyl**

Add non-root `containerUser`, uppercase environment-map keys, a discriminated runtime protocol union, `modelhub-shell`, and optional `seedConfigPath`. Update Sibyl manifests with `containerUser: sibyl`, `environment.SIBYL_ENVIRONMENT: dev`, `protocol: http`, and `seedConfigPath: config/appconfig/sibyl/wg-dev.yaml`.

- [ ] **Step 4: Write failing stack and release tests**

Assert numeric ModelHub user, application and AppConfig environments, only 50053, gRPC health command, caller ingress, private Cloud Map, and no load balancer. Prove HTTP still curls while gRPC requires DNS/TCP plus healthy ECS task/container status.

- [ ] **Step 5: Generalize stack and release**

Replace hard-coded Sibyl user/environment. Load and scan a seed only when declared; always require repository schema. HTTP gets a readiness URL; gRPC gets `host:port`. In gRPC mode, resolve Cloud Map, test TCP, and require every running task and application container to be `HEALTHY`; the container command performs standard gRPC Health.

- [ ] **Step 6: Add the ModelHub build contract**

```bash
EXPECTED_REVISION="$COMMIT_SHA" PLATFORM_IMAGE="$production_image" \
  bash "$CONTAINER_CONTRACT_PATH"
```

Continue producing the separate migration target and its immutable digest in `release.json`.

- [ ] **Step 7: Run infrastructure tests**

```bash
npm test -- test/stacks/managed-internal-service-stack.test.ts test/security/build-release-contracts.test.ts test/config/schema.test.ts
npm run build
```

Expected: PASS.

- [ ] **Step 8: Commit generic support**

```bash
git add lib buildspec scripts config/services/sibyl.yaml test
git diff --cached --check
git commit -m "feat(platform): support managed internal gRPC services"
```

---

### Task 6: Onboard ModelHub in `wgPlatformInfra`

**Files:**
- Create: `config/services/modelhub.yaml`
- Create: `config/appconfig/modelhub/schema.json`
- Create: `test/services/modelhub.test.ts`
- Create: `docs/runbooks/modelhub-first-release.md`
- Modify: `bin/app.ts`
- Modify: `test/smoke/app.test.ts`

**Interfaces:**
- Consumes: committed ModelHub SHA and contract hashes.
- Produces: `wg-dev-modelhub-service`, `wg-dev-modelhub`, `wg/modelhub`, `modelhub-service`, and `modelhub.internal.dev`.

- [ ] **Step 1: Compute immutable checkpoints**

```bash
git -C /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev rev-parse HEAD
shasum -a 256 /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev/Dockerfile
shasum -a 256 /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev/scripts/platform/verify_platform_image.sh
shasum -a 256 /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev/schema/appconfig.schema.json
```

Copy the exact outputs to manifest/tests; never use a mutable branch or tag as a checkpoint.

- [ ] **Step 2: Write failing ModelHub service tests**

Assert repo/branch/auto-deploy, computed hashes, exact AppConfig IDs, no seed path or secret references, gRPC 50053, `10001:10001`, 2 tasks, 1024/2048, migration target, database approval, and exact caller SG. The copied schema hash must equal the business schema hash.

- [ ] **Step 3: Add manifest and schema**

```yaml
environment:
  WG_CONFIG_SOURCE: appconfig
  WG_SERVER_GRPC_PORT: "50053"
```

The stack adds `APP_NAME=modelhub`, `ENV=dev`, `SERVICE_NAME=config-dev`, and `REGION=us-east-2`. Do not set `WG_SERVER_PUBLIC_GRPC_PORT`. Use `releaseGate: none`, auto-deploy true, and migration target `migration`.

- [ ] **Step 4: Register ModelHub in CDK app**

Allow exactly `modelhub,recommend,sibyl`; route managed manifests through `ManagedInternalServiceStack` while preserving Recommend's existing-service path. Assert stack creation and no ModelHub load balancer.

- [ ] **Step 5: Write first-release runbook**

Include immutable evidence, AppConfig validator metadata/update, secret-safe version validation, bounded CDK diff/deploy, database check, explicit `001` migration with digest, pipeline activation, Cloud Map/gRPC verification, streaming smoke test, Logfire `server`, and rollback. Forbid `002` and `003`.

- [ ] **Step 6: Run full infra suite and synth**

```bash
npm test
npm run build
npm run schemas
npm run validate
npx cdk synth wg-dev-modelhub-service --context environment=wg-dev
```

Expected: PASS, with no public ModelHub resource.

- [ ] **Step 7: Commit onboarding**

```bash
git add config/appconfig/modelhub config/services/modelhub.yaml test/services/modelhub.test.ts docs/runbooks/modelhub-first-release.md bin/app.ts test/smoke/app.test.ts
git diff --cached --check
git commit -m "feat(platform): onboard ModelHub dev"
```

---

### Task 7: Verify and review both repositories

**Files:**
- Modify only for validated in-scope findings.

**Interfaces:**
- Consumes: completed application and infrastructure branches.
- Produces: push-ready reviewed commits.

- [ ] **Step 1: Verify ModelHub**

```bash
go test ./config ./internal/dbmigration ./cmd/healthcheck ./internal/apikeystore ./internal/auth ./internal/infra/factory ./internal/service/modelhub ./internal/taskstore ./models -count=1
go test ./... -count=1
go vet ./...
docker build --platform linux/amd64 --label "org.opencontainers.image.revision=$(git rev-parse HEAD)" -t modelhub:local .
EXPECTED_REVISION="$(git rev-parse HEAD)" PLATFORM_IMAGE=modelhub:local bash scripts/platform/verify_platform_image.sh
docker build --platform linux/amd64 --target migration -t modelhub-migration:local .
```

Focused tests, vet, builds, and image contract must pass. Full suite may contain only the three approved FFmpeg failures.

- [ ] **Step 2: Verify infrastructure**

```bash
npm ci
npm test
npm run build
npm run schemas
npm run validate
npx cdk synth --context environment=wg-dev
```

Expected: PASS.

- [ ] **Step 3: Review both branch diffs**

Review `git diff main...HEAD` for unrelated files, secrets, Nacos regressions, public exposure, automatic DDL, IAM expansion, mutable images, rollback gaps, and contamination from the original dirty infra checkout.

- [ ] **Step 4: Request code review**

Use `requesting-code-review`; apply validated findings, rerun affected tests, and commit focused fixes.

- [ ] **Step 5: Stop for push confirmation**

Present branches, commits, tests, known failures, CDK summary, and exact remote refs. Ask before any push.

---

### Task 8: Push, provision, migrate, and deploy

**Files:**
- External state only after Task 7 confirmation.

**Interfaces:**
- Consumes: approved commits and AWS profile `wgdl-new`.
- Produces: healthy `modelhub-service` plus automatic `wg-dev-modelhub` delivery.

- [ ] **Step 1: Push approved branches**

Push `wgModelHub/dev` and the infra feature branch without force, then integrate infra according to repository policy.

- [ ] **Step 2: Validate identity and AppConfig metadata**

Require account `054043816891`; describe the exact Ohio AppConfig IDs, hosted-version metadata, validator metadata, and deployed version without outputting content.

- [ ] **Step 3: Review CDK diff**

Accept only scoped ECR, IAM, logs, SG, Cloud Map, ECS, CodeBuild, and CodePipeline resources. Reject public IP, load balancer, 50054, broad CIDR, unrelated stacks, or secrets.

- [ ] **Step 4: Deploy desired-count-zero infrastructure**

Deploy only approved stacks; verify ECR/pipeline/ECS/roles/SG and no bootstrap task.

- [ ] **Step 5: Build immutable images**

Run the exact dev SHA through `wg-dev-modelhub`; capture production/migration digests and scans; keep desired count zero.

- [ ] **Step 6: Run approved migration `001`**

Show exact digest, task definition, network, roles, SQL hash, advisory lock, RDS metadata, and rollback evidence. Run one digest-pinned task with Agent; require exit zero and a read-only table check. Never run `002` or `003`.

- [ ] **Step 7: Activate and verify**

Set desired count two through managed release. Require two healthy tasks, Agent health, digest equality, Cloud Map resolution, gRPC Health `SERVING`, and no public address/listener.

- [ ] **Step 8: Run streaming smoke test**

From an approved VPC caller, select one low-cost configured text model without printing config. Require incremental events plus one final event and verify invalid-model normalization.

- [ ] **Step 9: Verify observability and automation**

Confirm CloudWatch and Logfire `server` metadata without sensitive data. Confirm source `wgdl666/wgModelHub/dev`, push detection, and `QUEUED` mode.

- [ ] **Step 10: Record final evidence**

Record only SHAs, digests, task definition, AppConfig version, migration task ARN/exit, ECS counts, Cloud Map, pipeline execution, and redacted trace IDs.
