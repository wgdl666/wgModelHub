# wgModelHub AWS dev deployment design

**Date:** 2026-08-27

**Target service:** `wgModelHub`

**Target environment:** AWS account `054043816891`, Singapore `ap-southeast-1`, ECS cluster `wg-dev`

**Source branch:** `dev`, created from the latest `main`

**Configuration control plane:** AWS AppConfig in `us-east-2`

## 1. Goal

Deploy `wgModelHub` to the Singapore `wg-dev` ECS cluster as an internal-only gRPC service. The same application must remain compatible with the existing Aliyun ACK/Nacos deployment while the AWS deployment loads all runtime configuration from the already-created AppConfig resources.

Every push to `dev` must automatically test, build, scan, release, and verify an immutable image. Database DDL must remain an explicit deployment operation and must never run during application startup.

## 2. Confirmed scope

The delivery includes:

- a configuration-source boundary selected by environment variable;
- unchanged Nacos behavior for the existing deployment;
- fail-closed AppConfig loading through a local AppConfig Agent sidecar;
- an internal ECS Fargate service with two tasks and Cloud Map discovery;
- a `dev`-triggered CodePipeline following the existing Recommend/Sibyl delivery model;
- an explicit, one-time migration task for `001_generation_task.sql`;
- repository and infrastructure tests, image contract verification, rollout verification, and rollback controls.

The delivery explicitly excludes:

- a public load balancer, public DNS, TLS termination, or port `50054` exposure;
- API Key creation, rotation, or revocation tooling;
- changes to `wgDevPlatform`;
- execution of public API Key migrations `002_modelhub_api_key.sql` and `003_modelhub_api_key_expires_nullable.sql`;
- automatic DDL from the ModelHub server process;
- changes to the existing Gemini proxy behavior. A blank `proxy_url` continues to mean direct access, which is the AWS configuration.

## 3. Existing state

The current `main` implementation:

- exposes gRPC on internal port `50053` and optionally exposes authenticated gRPC on `50054`;
- always starts through a Nacos bootstrap file and loads Data ID `wg.mirror.modelHub`;
- applies strictly validated YAML and supports limited Nacos hot updates;
- requires `WG_SERVER_GRPC_PORT` and accepts optional `WG_SERVER_PUBLIC_GRPC_PORT`;
- opens PostgreSQL at startup but does not execute migrations;
- maps ModelHub failures to gRPC status plus `google.rpc.ErrorInfo`;
- already treats an empty Gemini `proxy_url` as direct access.

The AWS control plane already contains:

| Resource | Value |
| --- | --- |
| AppConfig region | `us-east-2` |
| Application | `modelhub` (`u8e7hop`) |
| Environment | `dev` (`mk0jmep`) |
| Configuration profile | `config-dev` (`u1tlyq1`) |
| ECS region | `ap-southeast-1` |
| ECS cluster | `wg-dev` |
| Shared approved caller security group | `sg-09930b896aeeb910f` |

The configuration profile is hosted and currently has no JSON Schema validator. Infrastructure must attach the repository-owned schema without printing or committing the hosted configuration body.

## 4. Configuration-source architecture

### 4.1 Source selection

Introduce a small configuration loader interface at the server startup boundary. Selection is controlled by `WG_CONFIG_SOURCE`:

| Value | Behavior |
| --- | --- |
| unset or `nacos` | Preserve the current bootstrap, initial fetch, listener, and hot-update behavior. |
| `appconfig` | Fetch once from the local AppConfig Agent, validate, and start without a configuration listener. |
| any other value | Fail startup with a configuration-source error. |

The unset default is deliberately `nacos` so the current ACK manifest remains backward-compatible.

### 4.2 AWS identity environment

The AWS task injects only source identity and runtime topology:

```text
WG_CONFIG_SOURCE=appconfig
APP_NAME=modelhub
ENV=dev
SERVICE_NAME=config-dev
REGION=us-east-2
WG_SERVER_GRPC_PORT=50053
```

`WG_SERVER_PUBLIC_GRPC_PORT` is absent. Provider credentials, provider URLs, database DSN, model routes, and Logfire configuration remain exclusively in AppConfig.

### 4.3 Agent access and fail-closed rules

The loader calls the AppConfig Agent at the loopback endpoint using the fixed resource path:

```text
/applications/modelhub/environments/dev/configurations/config-dev
```

The loader must:

- accept only a loopback Agent host;
- use a short bounded timeout;
- reject redirects;
- cap the response size;
- reject empty responses and multiple YAML documents;
- use the same known-field YAML parser and `Config.Validate` path as Nacos;
- never log the response body or secret values;
- fail startup instead of falling back to Nacos, a local file, or configuration environment variables.

AppConfig changes use `reloadMode: restart`. A configuration deployment takes effect after an ECS rolling restart. Nacos keeps its existing listener and restart-required-field behavior.

### 4.4 Agent lifecycle

The AppConfig Agent is an essential sidecar in the same Fargate task. It prefetches `modelhub/dev/config-dev` and becomes healthy before the application container starts. The task role can call only `appconfig:StartConfigurationSession` and `appconfig:GetLatestConfiguration` for the three imported AppConfig resource IDs.

## 5. ECS runtime architecture

Deploy a managed internal Fargate service with this contract:

| Setting | Value |
| --- | --- |
| ECS service | `modelhub-service` |
| Task family | `modelhub-dev` |
| Application container | `modelhub` |
| ECR repository | `wg/modelhub` |
| Platform | `linux/amd64` |
| Container identity | `10001:10001` |
| CPU | 1024 units |
| Memory | 2048 MiB |
| Desired count | 2 after migration |
| Application port | gRPC `50053` |
| Service discovery | `modelhub.internal.dev` |
| Public IP | disabled |
| Deployment | rolling update with ECS circuit breaker and rollback |

The service runs in the existing private subnets. Its security group accepts TCP `50053` only from `sg-09930b896aeeb910f`, which currently identifies the shared approved application and verification callers. It does not accept the entire VPC CIDR.

Standard gRPC Health is the container health and readiness contract. Release verification resolves `modelhub.internal.dev:50053` from inside the VPC and requires `SERVING` before a deployment is accepted.

## 6. Build and release pipeline

Create a `wg-dev-modelhub` V2 CodePipeline with `QUEUED` execution mode and automatic source detection:

```text
wgdl666/wgModelHub dev push
  -> Test
  -> Build and image contract verification
  -> ECR vulnerability scan
  -> Managed ECS release
  -> VPC gRPC verification
```

### 6.1 Test stage

The test stage verifies:

- `go test ./...`;
- strict AppConfig schema and loader behavior;
- the repository AppConfig JSON Schema hash;
- configuration-source compatibility and Nacos regression coverage.

### 6.2 Build stage

The build stage produces `linux/amd64` images tagged `git-<40-character-source-commit>`. It adds OCI source-revision and contract hashes, verifies the image contract before push, scans the ECR image, and releases only by immutable digest.

The image contract requires:

- numeric non-root user `10001:10001`;
- exposed internal gRPC port `50053`;
- the expected server entry point;
- the exact source revision;
- no local configuration, bootstrap file, source history, migration credentials, or high-confidence secret material in the exported image filesystem.

### 6.3 Release and verification

`source.autoDeploy` is enabled and the release gate is `none` after initial provisioning. The managed release registers a new task definition containing the immutable digest, rolls the two tasks, waits for ECS stability, checks gRPC readiness, and verifies that the running task digest matches the build artifact.

If tasks do not become healthy, the ECS circuit breaker rolls back to the previous task definition and the pipeline fails visibly.

## 7. Database migration

The application server must continue to contain no `Schema.Create`, automatic migration, or startup DDL path.

The first AWS release uses an independently invoked migration target:

1. Provision the ECS service with desired count zero.
2. Start the AppConfig Agent and migration container as a one-time Fargate task.
3. Load the database DSN from the same `modelhub/dev/config-dev` AppConfig document.
4. Acquire a PostgreSQL advisory lock to prevent concurrent migration execution.
5. Execute only `migrations/001_generation_task.sql` inside a transaction.
6. Record success or failure in the dedicated migration log group without logging the DSN or SQL parameter values.
7. Start the two service tasks only after a successful migration task exit.

The migration is idempotent because `001` uses `CREATE TABLE IF NOT EXISTS`. Public API Key migrations remain unexecuted until a separate public-access design is approved.

## 8. AppConfig schema and secret handling

Add `schema/appconfig.schema.json` to `wgModelHub`. It must describe the current strict YAML shape, require the database, Logfire, provider, and model-routing constraints needed at runtime, and allow provider credential fields without embedding any credential value.

Attach that schema as the validator for the existing `config-dev` profile. Before infrastructure activation, fetch the current hosted version into a permission-restricted temporary location, validate it without printing it, and remove the temporary copy. Validation failures stop deployment and report only field paths and non-secret validation reasons.

The Agent response, configuration body, DSN, provider credentials, prompts, media, and API responses must never enter CodeBuild output, CloudWatch logs, deployment artifacts, or Logfire spans.

## 9. Observability and failure behavior

- Application logs use a dedicated `/ecs/wg-modelhub` log group.
- Agent and migration logs use separate log groups and stream prefixes.
- AWS backend telemetry uses Logfire project `server` and the service identity supplied by AppConfig.
- Traces carry standard TraceContext but exclude prompts, media, configuration bodies, and credentials.
- AppConfig Agent failure keeps the task unhealthy.
- Invalid AppConfig causes a fast, explicit startup failure with no fallback.
- Provider and database startup errors cause task failure and ECS rollback.
- Runtime provider errors keep the existing normalized gRPC status and `ErrorInfo` contract.

## 10. Test plan

### 10.1 Application tests

- source selection: unset, `nacos`, `appconfig`, and unknown value;
- successful AppConfig Agent response;
- loopback endpoint enforcement;
- timeout, connection failure, redirect, oversize body, empty body, invalid YAML, multiple documents, and unknown field rejection;
- no AppConfig-to-Nacos fallback;
- unchanged Nacos initial load, listen, hot-apply, and restart-required rejection;
- server startup with internal port only;
- image contract and secret scan.

### 10.2 Infrastructure tests

- imported AppConfig IDs and least-privilege IAM;
- essential and healthy Agent dependency;
- two-task Fargate service, CPU, memory, private subnet, and no public IP;
- exact security-group source and port;
- Cloud Map name and gRPC port;
- automatic `dev` pipeline, immutable ECR digest, and circuit breaker;
- desired count zero before migration and two after activation;
- absence of load balancers and public listener resources.

### 10.3 Deployment smoke test

After rollout:

1. require two running and healthy ECS tasks;
2. require AppConfig Agent health in both tasks;
3. resolve `modelhub.internal.dev` inside the VPC;
4. require gRPC Health `SERVING` on `50053`;
5. issue one minimal streaming `Generate` using a low-cost text model already declared by AppConfig;
6. require incremental data followed by exactly one final event;
7. confirm CloudWatch and Logfire contain metadata and traces but no prompt, media, DSN, or credential values;
8. confirm there is no internet-facing listener or public task address.

## 11. Repository changes

### `wgModelHub`

- configuration loader abstraction and AppConfig Agent loader;
- server startup selection and lifecycle wiring;
- JSON Schema and tests;
- migration command and image target for explicit `001` execution;
- platform service metadata and image verification script;
- Docker image contract updates and deployment documentation.

### `wgPlatformInfra`

- `modelhub` service manifest and schema support needed for a multi-container internal gRPC service;
- AppConfig imports and validator association;
- ECS, Cloud Map, IAM, logs, security groups, ECR, Pipeline, migration task, and gRPC verification;
- CDK unit, schema, security, and synth tests.

The existing dirty `wgPlatformInfra` checkout is user-owned. Implementation must use an isolated worktree and must not overwrite or include those unrelated changes.

## 12. Rollout and rollback

The initial rollout order is:

1. merge and provision infrastructure with desired count zero;
2. run the explicit `001` migration and verify exit zero;
3. run `wg-dev-modelhub` for the selected `dev` commit; its managed release activates desired count two;
4. complete the internal health and streaming smoke tests;
5. observe ECS, CloudWatch, and Logfire before declaring the service healthy.

Application rollback redeploys the last healthy task definition and immutable digest. Configuration rollback deploys the previous AppConfig hosted version and restarts ECS. Neither rollback reverses `001`; it is backward-compatible and idempotent.

## 13. Acceptance criteria

The work is complete when all of the following are true:

- `dev` exists from the latest `main` and automatically triggers `wg-dev-modelhub`;
- the existing Nacos deployment behavior remains compatible;
- AWS tasks load all runtime configuration only from `modelhub/dev/config-dev`;
- no configuration secret or body appears in source, artifacts, or logs;
- `001_generation_task.sql` was executed only by the approved migration task;
- two healthy ModelHub tasks run in private subnets;
- `modelhub.internal.dev:50053` serves gRPC Health and a minimal streaming request from an approved caller;
- port `50054` has no ECS mapping or public exposure;
- no public load balancer, DNS, TLS, or API Key management was created;
- a failed application or configuration rollout demonstrably fails the pipeline and preserves or restores the prior healthy task definition.

## 14. Known baseline issue

The clean latest-`main` baseline has three pre-existing local failures in Gemini video tests because their mock MP4 bytes are rejected by the locally installed FFmpeg with `moov atom not found`:

- `TestGenerateI2VInteractionPayload`;
- `TestGenerateI2VURIOutputDownloadsWithAuth`;
- `TestGenerateEditUploadsFilesAndOrdersParts`.

The user explicitly approved continuing without fixing these three tests. The implementation must not introduce additional failures, and the pipeline design must still address how these tests execute deterministically in CodeBuild.
