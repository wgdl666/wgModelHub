# GPT Image 2 Client and Post-Deployment Smoke Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a safe Go example for the internal `gpt-image-2` gRPC API and run that same client once after every successful `wgModelHub` dev release, with warning-only failure handling.

**Architecture:** `wgModelHub` owns a small CLI that builds the fixed ModelHub image request, validates the complete server stream, and atomically writes one image. `wgPlatformInfra` adds a narrowly typed optional smoke capability, a private CodeBuild project with its own security group, and a final pipeline stage that invokes the repository wrapper after `ManagedRelease`. The smoke publishes sanitized SNS warnings and never rolls back the application release.

**Tech Stack:** Go 1.26, gRPC 1.78, protobuf, Bash, AWS CodeBuild, CodePipeline V2, SNS, VPC security groups, AWS CDK 2.263.0, TypeScript 5.9, Vitest 4.1.

## Global Constraints

- ModelHub worktree: `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev`, branch `dev`, starting at design commit `6a411ba`.
- Infrastructure worktree: `/Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub`, branch `feat/modelhub-dev`, starting at `1a0031b`.
- Do not modify the user-owned checkout `/Users/bruce/workspaces/wgdl_aws/wgPlatformInfra`.
- The only endpoint is internal plaintext gRPC `modelhub.internal.dev:50053`; do not add public access, TLS termination, port `50054`, or `authorization` metadata.
- The request model is always `models.GPTImage2`; the live smoke is text-to-image only, `1:1`, `1024x1024`, with a five-minute call timeout and no retry.
- Receive at most `protocol.MaxRPCMessageBytes` and accept exactly one inline safe-ASCII `image/<token>` (including `image/avif`, but no empty subtype, parameter, whitespace, control, or non-ASCII byte) whose bytes are non-empty and at most `protocol.MaxMediaBytes`.
- Never log prompts, image bytes, full gRPC/provider responses, provider details, AppConfig content, DSNs, or credentials.
- The pipeline fixed prompt is `A centered red paper boat floating on calm blue water, simple studio illustration, no text`, but it must not be printed in CodeBuild logs or SNS.
- Pipeline-generated files live only in a mode-`0700` temporary directory and are deleted on success and failure. No image artifact is emitted.
- Controlled smoke/bootstrap failures after the repository-owned build command starts make at most one fixed warning attempt to `wg-dev-cicd-failures` and exit zero. The bootstrap executes only the non-empty decoded script whose construct-supplied SHA-256 matches, and keeps coordination markers in its own mode-`0700` directory. An internal 510-second watchdog, bounded cleanup, and bounded SNS call leave margin before the ten-minute CodeBuild timeout. CodeBuild-managed container/runtime initialization before the build command begins is an uncapturable platform-startup exception, not a functional/setup failure. The Smoke stage has no rollback rule.
- Add no AppConfig, database, migration, ECS mutation, ECR push, Secrets Manager, SSM parameter, or provider-secret permission to the smoke role.
- Keep the running service at two private replicas; do not change AppConfig or any database schema.
- Use TDD for every behavior change, review each staged diff before commit, ask before pushing either repository, and show the infrastructure diff before AWS deployment.

---

### Task 1: Add the stream-validating GPT Image 2 client core

**Files:**
- Create: `examples/gpt-image-2/client.go`
- Create: `examples/gpt-image-2/client_test.go`

**Interfaces:**
- Consumes: `modelhubv2.ModelHubServiceClient`, `models.GPTImage2`, `protocol.MaxRPCMessageBytes`, and `protocol.MaxMediaBytes`.
- Produces: `generateImage(context.Context, modelhubv2.ModelHubServiceClient, string) (imageResult, *smokeFailure)` and `writeImage(string, []byte, bool) *smokeFailure`.

- [ ] **Step 1: Write the failing request and valid-response tests**

Create a `bufconn` test server that records the `GenerateRequest`, sends one final event containing `image/png` inline bytes, and closes normally. Assert this exact request shape:

```go
if got.GetModel() != models.GPTImage2 {
	t.Fatalf("model=%q", got.GetModel())
}
message := got.GetInput().GetItems()[0].GetMessage()
if message.GetRole() != modelhubv2.Role_ROLE_USER || message.GetParts()[0].GetText() != prompt {
	t.Fatalf("input=%#v", got.GetInput())
}
image := got.GetOutput().GetImage()
if image == nil || image.GetAspectRatio() != "1:1" || image.GetImageSize() != "1024x1024" {
	t.Fatalf("output=%#v", got.GetOutput())
}
```

Assert the returned `imageResult` has MIME `image/png` and exact response bytes. Add a second success case close to, but still below, `protocol.MaxMediaBytes` to exercise the application-level media limit independently of transport setup.

- [ ] **Step 2: Run the focused test and capture the expected failure**

```bash
go test ./examples/gpt-image-2 -run 'TestGenerateImage' -count=1
```

Expected: compile failure because `generateImage`, `imageResult`, and `smokeFailure` do not exist.

- [ ] **Step 3: Define stable result and failure types**

Implement these exact boundaries in `client.go`:

```go
type failureCategory string

const (
	failureSourceValidation failureCategory = "source-validation"
	failureConnect          failureCategory = "connect"
	failureRPC              failureCategory = "rpc"
	failureTimeout          failureCategory = "timeout"
	failureProtocol         failureCategory = "protocol"
	failureOutput           failureCategory = "output"
)

type smokeFailure struct {
	category failureCategory
	grpcCode codes.Code
}

func (failure *smokeFailure) Error() string {
	if failure.grpcCode != codes.OK {
		return fmt.Sprintf("gpt-image-2 failed: category=%s grpc_code=%s", failure.category, failure.grpcCode)
	}
	return fmt.Sprintf("gpt-image-2 failed: category=%s", failure.category)
}

type imageResult struct {
	mimeType string
	data     []byte
}
```

Do not retain or format an upstream error message inside `smokeFailure`.

- [ ] **Step 4: Implement the fixed request and complete-stream validator**

Build the request only through generated protobuf types:

```go
request := &modelhubv2.GenerateRequest{
	Model: models.GPTImage2,
	Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
		Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
			Role: modelhubv2.Role_ROLE_USER,
			Parts: []*modelhubv2.ContentPart{{
				Content: &modelhubv2.ContentPart_Text{Text: prompt},
			}},
		}},
	}}},
	Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{
		Image: &modelhubv2.ImageOutput{
			AspectRatio: proto.String("1:1"),
			ImageSize:   proto.String("1024x1024"),
		},
	}},
}
```

Read through `io.EOF`. Track `finalSeen` and image count. Reject missing/multiple final events, any event after final, zero or multiple images, URI sources, MIME outside the safe ASCII `image/<token>` grammar, empty data, and data larger than `protocol.MaxMediaBytes`. Map `codes.DeadlineExceeded` to `timeout`; map other gRPC failures to `rpc` without preserving the message.

- [ ] **Step 5: Add the complete invalid-stream table**

Add named cases for `missing-final`, `multiple-finals`, `event-after-final`, `missing-image`, `multiple-images`, `uri-image`, `missing-mime`, `non-image-mime`, `empty-data`, `oversize-data`, `recv-error`, and `diagnostic-only`. Each case must assert `failure.category == failureProtocol`, except deadline and non-EOF stream status cases, which assert `timeout` and `rpc` respectively. Include diagnostic text alongside a valid image as a success case. Add a table that rejects video, tool-call, nil output items, absent/unknown oneofs, and nil-image wrappers both before and after an otherwise valid image.

- [ ] **Step 6: Write failing atomic-output tests**

Test a successful write, a missing parent directory, an existing destination without force, overwrite with force, and a write failure. Success must preserve exact bytes and mode `0600`; every failure must leave no file matching the temporary prefix.

- [ ] **Step 7: Implement atomic output**

`writeImage` must `os.Stat` the parent directory, reject a pre-existing target unless `force`, create a temporary file in that same directory, `Chmod(0600)`, write, `Sync`, and close it. With `force`, publish using atomic `Rename`. Without `force`, publish with an atomic same-directory hard link and then unlink the temporary name, so a destination created after the initial check is never clobbered. A defer removes the temporary file until publication succeeds. Return only `failureOutput` on any filesystem failure.

- [ ] **Step 8: Run the client-core tests**

```bash
go test ./examples/gpt-image-2 -run 'TestGenerateImage|TestWriteImage' -count=1
```

Expected: PASS.

- [ ] **Step 9: Review and commit the client core**

```bash
gofmt -w examples/gpt-image-2/client.go examples/gpt-image-2/client_test.go
git add examples/gpt-image-2/client.go examples/gpt-image-2/client_test.go
git diff --cached --check
git diff --cached
git commit -m "feat(example): add gpt-image-2 grpc client"
```

---

### Task 2: Add the CLI, safe shell wrapper, and usage documentation

**Files:**
- Create: `examples/gpt-image-2/main.go`
- Create: `examples/gpt-image-2/main_test.go`
- Create: `examples/gpt-image-2/wrapper_test.go`
- Create: `scripts/examples/gpt-image-2.sh`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 1 `generateImage`, `writeImage`, `smokeFailure`, and the generated gRPC client.
- Produces: `parseArgs([]string) (clientConfig, *smokeFailure)`, `dialContextFunc`, `run(context.Context, []string, io.Writer, dialContextFunc) *smokeFailure`, stable process exit codes, and `scripts/examples/gpt-image-2.sh`.

- [ ] **Step 1: Write failing CLI argument tests**

Define tests for required `--address`, `--prompt`, and `--output`; default `--timeout=5m`; positive custom timeout; `--force`; unknown flags; blank values; zero/negative timeout; and extra positional arguments. Valid parsing must produce:

```go
clientConfig{
	address: "modelhub.internal.dev:50053",
	prompt:  "red paper boat",
	output:  "image.png",
	timeout: 5 * time.Minute,
	force:   false,
}
```

Invalid cases return `failureSourceValidation` and never echo the offending prompt.

- [ ] **Step 2: Run the CLI tests and verify failure**

```bash
go test ./examples/gpt-image-2 -run 'TestParseArgs|TestRun' -count=1
```

Expected: compile failure because `clientConfig`, `parseArgs`, and `run` do not exist.

- [ ] **Step 3: Implement parsing, dialing, and safe summary output**

Use a `flag.FlagSet` whose output is `io.Discard`. Define the testable dial boundary:

```go
type dialContextFunc func(context.Context, string, ...grpc.DialOption) (*grpc.ClientConn, error)
```

`run` creates one `context.WithTimeout`, then calls the supplied dialer; `main` supplies `grpc.DialContext`:

```go
conn, err := dial(
	callContext,
	config.address,
	grpc.WithTransportCredentials(insecure.NewCredentials()),
	grpc.WithBlock(),
	grpc.WithDisableRetry(),
	grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(protocol.MaxRPCMessageBytes)),
)
```

Call `generateImage`, then `writeImage`. Emit exactly one success line:

```text
mime_type=image/png bytes=12345 output=/requested/path.png
```

The error line produced by `main` contains only `smokeFailure.Error()`. Map process exit codes exactly:

| Category | Exit code |
| --- | ---: |
| `source-validation` | 2 |
| `connect` | 20 |
| `rpc` | 21 |
| `timeout` | 22 |
| `protocol` | 23 |
| `output` | 24 |

If the blocking dial fails because its context expires, return `failureTimeout`; otherwise return `failureConnect`. Keep `grpc.WithDisableRetry()` explicit so even a resolver-supplied service config cannot enable application retries.

- [ ] **Step 4: Test successful execution and sanitized failures**

Use the same `bufconn` server through `dialContextFunc`. Include a response larger than gRPC's default 4 MiB but within ModelHub limits to prove the configured receive limit works. Assert successful output contains only MIME, byte count, and destination. For every failure category, assert output/error strings do not contain the prompt, fake provider body, image bytes, or raw gRPC status message.

- [ ] **Step 5: Write the failing wrapper contract test**

Create a fake Go executable and set `WG_MODELHUB_GO_BIN` to it. The fake executable must create the requested `-o` binary; that binary records all forwarded arguments into a test file. Invoke the wrapper from a directory outside the repository with a test-owned `TMPDIR` and assert:

- the build output matches `$TMPDIR/wg-modelhub-gpt-image-2.XXXXXXXX/gpt-image-2`, where the eight `X` positions are replaced by `mktemp`, and `go build` ran from the repository root;
- every client argument arrived unchanged;
- the test output file remains after wrapper exit;
- the temporary binary and directory are removed;
- a fake build failure first emits a sensitive stderr sentinel, but the wrapper exits `70` and prints only `gpt-image-2 client build failed`;
- sending `INT`, `TERM`, or `HUP` only to the wrapper PID both after readiness and deterministically during the PID/PGID registration boundary terminates and reaps the active build/client child process group (including a descendant that ignores `TERM`), exits boundedly and explicitly as 130, 143, or 129 even if cleanup fails, and cleans the private build directory exactly once.

- [ ] **Step 6: Run the wrapper test and verify failure**

```bash
go test ./examples/gpt-image-2 -run 'TestWrapper' -count=1
```

Expected: FAIL because `scripts/examples/gpt-image-2.sh` does not exist.

- [ ] **Step 7: Implement the wrapper**

Use this control structure:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
go_bin=${WG_MODELHUB_GO_BIN:-go}
umask 077
tmp_dir=''
cleanup_done=0
active_pid=''
active_pgid=''
launch_in_progress=0
pending_signal_status=''
cleanup() {
  if ((cleanup_done)); then return; fi
  cleanup_done=1
  [[ -z "$tmp_dir" ]] || rm -rf -- "$tmp_dir"
}
stop_active_child() {
  [[ -n "$active_pid" ]] || return 0
  local pid=$active_pid
  local pgid=$active_pgid
  local attempt
  active_pid=''
  active_pgid=''
  kill -TERM -- "-$pgid" 2>/dev/null || true
  for attempt in {1..10}; do
    kill -0 -- "-$pgid" 2>/dev/null || break
    sleep 0.05
  done
  kill -KILL -- "-$pgid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
handle_signal() {
  local status=$1
  trap - EXIT INT TERM HUP
  stop_active_child
  cleanup || true
  exit "$status"
}
handle_exit() {
  local status=$1
  trap - EXIT INT TERM HUP
  stop_active_child
  cleanup || true
  exit "$status"
}
queue_or_handle_signal() {
  local status=$1
  if ((launch_in_progress)); then
    [[ -n "$pending_signal_status" ]] || pending_signal_status=$status
    return
  fi
  handle_signal "$status"
}
begin_child_launch() {
  pending_signal_status=''
  launch_in_progress=1
}
finish_child_launch() {
  local status
  launch_in_progress=0
  if [[ -n "$pending_signal_status" ]]; then
    status=$pending_signal_status
    pending_signal_status=''
    handle_signal "$status"
  fi
}
trap 'handle_exit $?' EXIT
trap 'queue_or_handle_signal 130' INT
trap 'queue_or_handle_signal 143' TERM
trap 'queue_or_handle_signal 129' HUP

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wg-modelhub-gpt-image-2.XXXXXXXX")
set -m
begin_child_launch
(cd -- "$repo_root" && exec "$go_bin" build -o "$tmp_dir/gpt-image-2" ./examples/gpt-image-2) >/dev/null 2>&1 &
active_pid=$!
active_pgid=$active_pid
finish_child_launch
set +m
build_status=0
wait "$active_pid" || build_status=$?
active_pid=''
active_pgid=''
if ((build_status != 0)); then
  printf '%s\n' 'gpt-image-2 client build failed' >&2
  exit 70
fi

set -m
begin_child_launch
"$tmp_dir/gpt-image-2" "$@" &
active_pid=$!
active_pgid=$active_pid
finish_child_launch
set +m
client_status=0
wait "$active_pid" || client_status=$?
active_pid=''
active_pgid=''
exit "$client_status"
```

Install traps while the temporary path is still empty. Treat `child &` through complete PID/PGID registration as a launch-critical section: traps queue the first pending signal instead of exiting, then dispatch it through `handle_signal` immediately after registration. Make `cleanup` idempotent; use a dedicated job-control process group for each active build/client; on signal or `EXIT`, send `TERM`, wait a short bounded grace period, escalate the group to `KILL`, reap the direct child, run cleanup once, and preserve the original/signal status even if cleanup fails. Never target the wrapper or caller process group. The production path never sets `WG_MODELHUB_GO_BIN`; it exists only to make the wrapper contract test deterministic.

- [ ] **Step 8: Document manual use**

Add a `GPT Image 2 internal example` section to `README.md` with the exact prerequisites and command:

```bash
./scripts/examples/gpt-image-2.sh \
  --address modelhub.internal.dev:50053 \
  --prompt "A small red paper boat floating on calm water" \
  --output ./gpt-image-2.png
```

State that the caller must be on the dev VPC/VPN, the parent directory must exist, the default timeout is five minutes, `--force` enables replacement, internal port `50053` needs no authorization header, and the command incurs one provider image-generation charge.

- [ ] **Step 9: Run focused and full ModelHub verification**

```bash
chmod +x scripts/examples/gpt-image-2.sh
go test ./examples/gpt-image-2 -count=1
go test ./... -count=1
go vet ./...
bash -n scripts/examples/gpt-image-2.sh
```

Expected: all commands PASS.

- [ ] **Step 10: Review and commit CLI, wrapper, and docs**

```bash
git add examples/gpt-image-2 scripts/examples/gpt-image-2.sh README.md
git diff --cached --check
git diff --cached
git commit -m "feat(example): add gpt-image-2 shell wrapper"
```

---

### Task 3: Add a bounded post-deployment smoke manifest contract

**Files:**
- Modify: `lib/config/schema.ts`
- Modify: `schema/service.schema.json` (generated)
- Modify: `config/services/modelhub.yaml`
- Modify: `test/services/modelhub.test.ts`
- Modify: `test/config/schema.test.ts`

**Interfaces:**
- Consumes: the existing `managedSchema` and generated JSON Schema workflow.
- Produces: optional `ManagedServiceManifest.postDeploySmoke` with `{ type: "modelhub-gpt-image-2"; failureMode: "warn" }`.

- [ ] **Step 1: Write failing manifest tests**

Assert the ModelHub manifest includes:

```ts
postDeploySmoke: {
  type: "modelhub-gpt-image-2",
  failureMode: "warn",
}
```

Add invalid fixture cases for unknown `type`, any `failureMode` other than `warn`, extra keys, an otherwise valid non-ModelHub gRPC service, a ModelHub HTTP runtime, wrong `runtime.cloudMapName`, and wrong `runtime.port`. Assert cross-field failures use path `["postDeploySmoke"]` and the precise intended message.

- [ ] **Step 2: Run focused schema tests and verify failure**

```bash
npx vitest run test/services/modelhub.test.ts test/config/schema.test.ts
```

Expected: FAIL because strict `managedSchema` rejects `postDeploySmoke`.

- [ ] **Step 3: Implement the manifest type and cross-field checks**

Add:

```ts
const postDeploySmokeSchema = z
  .object({
    type: z.literal("modelhub-gpt-image-2"),
    failureMode: z.literal("warn"),
  })
  .strict();
```

Extend the managed schema with `postDeploySmoke: postDeploySmokeSchema.optional()`. Add independent `superRefine` checks requiring `name === "modelhub"`, `runtime.protocol === "grpc"`, `runtime.cloudMapName === "modelhub"`, and `runtime.port === 50053`; every failure uses path `["postDeploySmoke"]` and a precise message. Do not add an arbitrary command, prompt, endpoint, model, timeout, or IAM field to YAML.

- [ ] **Step 4: Enable the capability and regenerate JSON Schema**

Add the exact two-key object to `config/services/modelhub.yaml`, then run:

```bash
npm run schemas
npm run validate
```

Expected: both commands PASS and `schema/service.schema.json` contains only the bounded enum/constant shape.

- [ ] **Step 5: Review and commit the manifest contract**

```bash
git add lib/config/schema.ts schema/service.schema.json config/services/modelhub.yaml test/services/modelhub.test.ts test/config/schema.test.ts
git diff --cached --check
git diff --cached
git commit -m "feat(modelhub): declare gpt image smoke"
```

---

### Task 4: Build the private warning-only smoke project

**Files:**
- Create: `lib/constructs/post-deploy-smoke.ts`
- Create: `buildspec/smoke-modelhub-gpt-image-2.yml`
- Create: `scripts/smoke-modelhub-gpt-image-2.sh`
- Create: `test/constructs/post-deploy-smoke.test.ts`
- Modify: `test/security/build-release-contracts.test.ts`
- Create: `test/security/smoke-runtime-contracts.test.ts`

**Interfaces:**
- Consumes: `IVpc`, private `SubnetSelection`, ModelHub service `ISecurityGroup`, port `50053`, endpoint `modelhub.internal.dev:50053`, and the warning topic ARN.
- Produces: `PostDeploySmoke.project: PipelineProject` and `PostDeploySmoke.securityGroup: SecurityGroup`.

- [ ] **Step 1: Write failing construct tests**

Instantiate the construct in a test stack and require a `BUILD_GENERAL1_SMALL` Amazon Linux 2023 CodeBuild project with a ten-minute timeout, VPC configuration, one dedicated security group, one TCP `50053` service ingress rule sourced only from that group, fixed environment variables, one topic-scoped `sns:Publish` statement, no CodeBuild report writes, and no ECS/AppConfig/SSM/Secrets Manager/RDS/ECR mutation/`iam:PassRole` action. Require exact failures before resource creation for any address other than `modelhub.internal.dev:50053` or port other than `50053`.

- [ ] **Step 2: Run the construct test and verify failure**

```bash
npx vitest run test/constructs/post-deploy-smoke.test.ts
```

Expected: compile failure because `PostDeploySmoke` does not exist.

- [ ] **Step 3: Write the failing shell contract tests**

Static tests must require the internal endpoint, repository wrapper, `--timeout 5m`, all eight stable failure categories, `gpt-image-2-smoke`, the 510-second watchdog, bounded cleanup/SNS calls, and the warning topic. Execute the smoke and complete buildspec command with fake tools. Cover success; exits `2`, `20`, `21`, `22`, `23`, `24`, and `70`; unset/empty Base64; empty/whitespace/wrong-identity decoded content; `mktemp`/decode/hash/`chmod`/dispatch failure; a hanging wrapper; `jq` failure; hanging/failing SNS; watchdog interruption during SNS; private-marker creation failure; and `INT`/`TERM`/`HUP`. Assert every controlled client/setup failure attempts at most one sanitized publish and exits zero, child marker failure itself performs zero SNS calls, every created temporary path is cleaned once, and signals perform idempotent cleanup with explicit status. Assert output omits the fixed prompt, fake provider response, output path, dynamic stderr, and sensitive sentinels. Add `image/avif` success plus empty-subtype, MIME-parameter, whitespace, control-character, and non-ASCII rejection cases.

- [ ] **Step 4: Implement the fixed smoke script**

Validate both `SOURCE_COMMIT` and `CODEBUILD_RESOLVED_SOURCE_VERSION` as the same lowercase 40-character SHA. Create a private temporary directory and call:

```bash
"$CODEBUILD_SRC_DIR/scripts/examples/gpt-image-2.sh" \
  --address "$SMOKE_ADDRESS" \
  --prompt "$fixed_prompt" \
  --output "$tmp_dir/image" \
  --timeout 5m >"$tmp_dir/client-summary" 2>"$tmp_dir/client-error"
```

Run the wrapper as a tracked child so signal handlers can terminate/wait it. Map wrapper/client exit codes to the allowed stable categories. On success, accept only a non-empty, whitespace/control-free `image/<token>` MIME and print MIME plus byte count. On failure, construct JSON with `jq -n`, publish once to the single topic through a tracked fifteen-second-bounded child, suppress AWS response output, clean up through a ten-second bound, and exit zero. Make cleanup/warning guards idempotent and install explicit `INT`, `TERM`, `HUP`, and `EXIT` handlers. Never enable shell tracing.

- [ ] **Step 5: Implement the embedded buildspec**

Use Go 1.26 and one opaque command block. Before `mktemp`, start a 510-second internal watchdog that covers Base64 decode, identity validation, `chmod`, script dispatch, wrapper build/module download, the five-minute RPC, summary validation, cleanup, and alerting. Create one mode-`0700` bootstrap directory, decode the smoke to a file inside it, require non-empty bytes, and compare `sha256sum` against the construct-supplied lowercase SHA-256 before execution. Catch every repository-controlled bootstrap error, suppress its dynamic stderr, clean the entire bootstrap directory at most once, publish one sanitized warning attempt, and exit zero. Keep no-clobber warning-attempt and alert-failed markers inside that private directory. A child that cannot create the attempt marker fails closed without SNS; the outer layer may then attempt once. A child `jq`/SNS failure records only the private alert marker, and the outer layer re-emits exactly `alert_publish_failed`; interrupting a hanging SNS child never triggers a second publish. Set warning `sourceCommit` only when the pipeline source and CodeBuild-resolved values are both lowercase 40-character hashes and equal, otherwise use `invalid`. Bound cleanup to ten seconds and SNS to fifteen seconds. Install idempotent `INT`, `TERM`, `HUP`, `USR1` watchdog, and `EXIT` handlers, and terminate/wait child processes before cleanup. Do not define an artifacts section.

The `install.runtime-versions` phase is CodeBuild-managed and runs before this command block. If container startup or managed Go runtime initialization fails before the command begins, repository handlers cannot run; keep that narrow condition documented as a platform-startup exception rather than expanding the setup/functional failure definition.

- [ ] **Step 6: Implement the construct**

Create a dedicated role and `PipelineProject` using `LinuxBuildImage.AMAZON_LINUX_2023_5`, `ComputeType.SMALL`, `privileged: false`, the private VPC/subnets, and the dedicated security group. Defensively reject any service port other than `50053` or address other than `modelhub.internal.dev:50053` before creating resources. Embed only `scripts/smoke-modelhub-gpt-image-2.sh` plus its build-time SHA-256; set the fixed address from the validated runtime contract. Set `grantReportGroupPermissions: false`; grant only `sns:Publish` to the warning topic beyond required CodeBuild VPC/log/source-artifact permissions.

- [ ] **Step 7: Run focused infrastructure tests**

```bash
npx vitest run test/constructs/post-deploy-smoke.test.ts test/security/build-release-contracts.test.ts test/security/smoke-runtime-contracts.test.ts
npm run build
```

Expected: PASS.

- [ ] **Step 8: Review and commit the smoke project**

```bash
git add lib/constructs/post-deploy-smoke.ts buildspec/smoke-modelhub-gpt-image-2.yml scripts/smoke-modelhub-gpt-image-2.sh test/constructs/post-deploy-smoke.test.ts test/security/build-release-contracts.test.ts test/security/smoke-runtime-contracts.test.ts
git diff --cached --check
git diff --cached
git commit -m "feat(pipeline): add private modelhub image smoke"
```

---

### Task 5: Wire the Smoke stage after ManagedRelease

**Files:**
- Modify: `lib/managed-internal-service-stack.ts`
- Modify: `test/services/modelhub.test.ts`
- Modify: `test/stacks/managed-internal-service-stack.test.ts`
- Modify: `test/services/sibyl.test.ts`

**Interfaces:**
- Consumes: Task 3 `postDeploySmoke`, Task 4 `PostDeploySmoke.project`, the pipeline `Source` artifact, and source action commit variable.
- Produces: pipeline stage `Smoke` with action `GPTImage2` after `Release`.

- [ ] **Step 1: Write the failing pipeline-order tests**

For ModelHub, require exact stage order:

```ts
expect(pipeline.Properties.Stages.map((stage: { Name: string }) => stage.Name)).toEqual([
  "Source",
  "Test",
  "Build",
  "Release",
  "Smoke",
]);
```

Require the Smoke stage to have one CodeBuild action named `GPTImage2`, one input artifact named `Source`, no output artifacts, run order 1, and no `OnFailure` rollback block. Require `SOURCE_COMMIT` to use the GitHub source action commit variable. Assert Sibyl and any manifest without `postDeploySmoke` still end at `Release`.

- [ ] **Step 2: Run stack tests and verify failure**

```bash
npx vitest run test/services/modelhub.test.ts test/stacks/managed-internal-service-stack.test.ts test/services/sibyl.test.ts
```

Expected: FAIL because the pipeline has no Smoke stage.

- [ ] **Step 3: Retain the source action and create the optional project**

Replace the inline source action with a named `sourceAction` so its `variables.commitId` can be passed to CodeBuild. When `props.manifest.postDeploySmoke` exists, create `PostDeploySmoke` with the schema-validated exact ModelHub Cloud Map target and port, VPC/private subnets, service security group, and warning topic ARN; the construct repeats the endpoint/port assertion defensively. Do not create the project or ingress for other manifests.

- [ ] **Step 4: Add the final pipeline stage**

After the existing Release stage, add:

```ts
pipeline.addStage({
  stageName: "Smoke",
  actions: [
    new CodeBuildAction({
      actionName: "GPTImage2",
      project: postDeploySmoke.project,
      input: source,
      environmentVariables: {
        SOURCE_COMMIT: { value: sourceAction.variables.commitId },
      },
    }),
  ],
});
```

Do not configure `onFailure`; the script owns warning-only functional failures.

- [ ] **Step 5: Run stack and complete infrastructure verification**

```bash
npx vitest run test/services/modelhub.test.ts test/stacks/managed-internal-service-stack.test.ts test/services/sibyl.test.ts
npm run schemas
npm run validate
npm test
npm run build
npx cdk synth wg-dev-modelhub-service --context environment=wg-dev >/tmp/wg-modelhub-smoke-synth.out
```

Expected: all commands PASS; synth contains the Smoke action, one new CodeBuild project, one smoke security group, and one TCP `50053` source-SG rule.

- [ ] **Step 6: Review and commit pipeline wiring**

```bash
git add lib/managed-internal-service-stack.ts test/services/modelhub.test.ts test/stacks/managed-internal-service-stack.test.ts test/services/sibyl.test.ts schema/service.schema.json
git diff --cached --check
git diff --cached
git commit -m "feat(modelhub): run image smoke after release"
```

---

### Task 6: Perform independent reviews and repository-wide verification

**Files:**
- Review only; fix files identified by concrete findings and add regression tests beside the affected code.

**Interfaces:**
- Consumes: all implementation commits from Tasks 1-5.
- Produces: review-clean ModelHub and infrastructure branches ready for push approval.

- [ ] **Step 1: Run ModelHub verification from a clean state**

```bash
git status --short
go test ./examples/gpt-image-2 -count=1
go test ./... -count=1
go vet ./...
bash -n scripts/examples/gpt-image-2.sh
git diff --check origin/dev...HEAD
```

Expected: clean worktree and every command PASS. The diff contains the approved design/plan, example client, wrapper, tests, and README only.

- [ ] **Step 2: Run infrastructure verification from a clean state**

```bash
npm ci
npm run schemas
npm run validate
npm test
npm run build
npx cdk synth wg-dev-modelhub-service --context environment=wg-dev >/tmp/wg-modelhub-smoke-synth.out
git status --short
git diff --check origin/feat/modelhub-dev...HEAD
```

Expected: clean worktree after generated schemas are committed and every command PASS.

- [ ] **Step 3: Run two-stage code review**

Dispatch a specification reviewer against the approved design and this plan, then a code-quality/security reviewer against each repository diff. Reviewers must explicitly check stream finality, 64 MiB receive configuration, atomic file behavior, log redaction, no retry, warning exit zero, stage order, no rollback, SG-only ingress, and least-privilege IAM.

- [ ] **Step 4: Resolve findings with tests first**

For each valid finding, add a failing regression test, run it to see the expected failure, implement the smallest correction, run focused/full verification, and commit with a scoped message. Do not apply speculative or unrelated refactors.

- [ ] **Step 5: Record exact push candidates**

```bash
git -C /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev log --oneline origin/dev..HEAD
git -C /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub log --oneline origin/feat/modelhub-dev..HEAD
```

Present these commits and verification results to the user and ask for one explicit push confirmation before either `git push`.

---

### Task 7: Push ModelHub and verify the pre-smoke deployment

**Files:**
- No repository changes.

**Interfaces:**
- Consumes: user push approval and the review-clean ModelHub `dev` branch.
- Produces: pushed client/wrapper commit and a healthy normal pipeline release before smoke infrastructure exists.

- [ ] **Step 1: Push only the reviewed ModelHub branch**

```bash
git -C /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgModelHub-dev push origin dev
```

Expected: `origin/dev` advances to the reviewed local `dev` HEAD without force.

- [ ] **Step 2: Follow the automatically triggered pipeline**

Use AWS profile `wgdl-new`, region `ap-southeast-1`, and pipeline `wg-dev-modelhub`. Capture the execution ID whose source revision equals the pushed 40-character SHA. Require `Source`, `Test`, `Build`, and `ManagedRelease` to succeed.

- [ ] **Step 3: Verify the deployed service remains healthy**

Require stack `wg-dev-modelhub-service` to be `UPDATE_COMPLETE`; ECS service `wg-dev/modelhub-service` to report desired/running/pending `2/2/0`, completed rollout, and zero failed tasks; both `modelhub` and `appconfig-agent` containers to be healthy; both application containers to run the exact new immutable digest; and tasks to have no public IP.

- [ ] **Step 4: Run the repository wrapper manually from the connected workstation**

Create a permission-restricted temporary directory outside the repository, invoke the documented wrapper once against `modelhub.internal.dev:50053`, inspect only MIME/byte count/path, verify the file signature matches the returned image MIME, calculate SHA-256, and remove the generated file after verification. Do not print the prompt or bytes. This is one billable provider attempt and has no retry.

---

### Task 8: Push and deploy the infrastructure smoke stage

**Files:**
- No new source changes unless the diff/review exposes a concrete defect.

**Interfaces:**
- Consumes: user push confirmation, review-clean `feat/modelhub-dev`, and successful Task 7 deployment.
- Produces: updated `wg-dev-modelhub-service` stack with the final Smoke stage.

- [ ] **Step 1: Push the reviewed infrastructure branch**

```bash
git -C /Users/bruce/workspaces/wgdl_aws/.codex-worktrees/wgPlatformInfra-modelhub push origin feat/modelhub-dev
```

Expected: remote branch advances without force.

- [ ] **Step 2: Produce and review the exact AWS diff**

```bash
AWS_PROFILE=wgdl-new AWS_REGION=ap-southeast-1 npx cdk diff wg-dev-modelhub-service --context environment=wg-dev
```

Expected additions are one CodeBuild project/role/log integration, one security group, one TCP `50053` source-security-group ingress rule, the optional Smoke stage/action, and one topic-scoped `sns:Publish` statement. There must be no replacement of the ECS service, no public resource, no AppConfig/database/migration change, and no broad IAM mutation.

- [ ] **Step 3: Present the change set and request deployment confirmation**

Summarize exact additions, IAM changes, target account/region/stack, absence of ECS replacement/database changes, and that the first validation execution will make one additional billable provider attempt. Do not deploy until the user confirms this concrete change set.

- [ ] **Step 4: Deploy the approved stack**

```bash
AWS_PROFILE=wgdl-new AWS_REGION=ap-southeast-1 npx cdk deploy wg-dev-modelhub-service --context environment=wg-dev --require-approval never
```

Expected: CloudFormation ends `UPDATE_COMPLETE`. Confirm the live pipeline stage order is `Source`, `Test`, `Build`, `Release`, `Smoke`; Release still contains only `ManagedRelease`; Smoke contains only `GPTImage2`.

---

### Task 9: Execute and verify the automatic GPT Image 2 smoke

**Files:**
- No repository changes.

**Interfaces:**
- Consumes: deployed Smoke stage and exact current `origin/dev` SHA.
- Produces: one verified end-to-end pipeline run and final operational report.

- [ ] **Step 1: Start one exact pipeline validation execution**

Start `wg-dev-modelhub` once and record the execution ID. Confirm its Source revision equals current `origin/dev`. Do not start a second execution if the smoke fails; the no-retry rule applies operationally as well.

- [ ] **Step 2: Wait for every stage and inspect sanitized results**

Require `ManagedRelease` to succeed before `GPTImage2` starts. Require the smoke CodeBuild output to contain only source/status/timing plus a valid `image/*` MIME and positive byte count, with no prompt, output bytes, credentials, AppConfig content, DSN, provider body, or raw gRPC message. Require no CodeBuild output artifact for the image.

- [ ] **Step 3: Re-verify ECS and network state**

Require two healthy internal tasks, healthy AppConfig sidecars, the expected immutable digest, no public IP, no load balancer, and Cloud Map `modelhub.internal.dev:50053`. Confirm no migration task is running or pending and no database operation occurred.

- [ ] **Step 4: Verify warning-only behavior without a paid second call**

Use infrastructure unit/contract test evidence rather than deliberately breaking the live endpoint. Confirm the deployed buildspec/script hashes match the reviewed sources and that all simulated client exit codes publish once and return zero. Do not induce a provider failure or publish a synthetic production warning unless separately requested.

- [ ] **Step 5: Write the deployment report**

Record source SHA, infrastructure SHA, CloudFormation status, pipeline execution ID, stage/action statuses, CodeBuild ID, MIME type, byte count, running task count, application image digest, absence of retained image artifacts, and confirmation that AppConfig/database/public-access state did not change. Exclude prompts, media, credentials, provider responses, and DSNs.
