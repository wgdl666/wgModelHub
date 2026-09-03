# GPT Image 2 client and post-deployment smoke design

**Date:** 2026-09-03

**Target service:** `wgModelHub`

**Target environment:** AWS account `054043816891`, Singapore `ap-southeast-1`, ECS cluster `wg-dev`

**Source branch:** `dev`

## 1. Goal

Add a repository-owned Go example client and shell wrapper that call the deployed internal `ModelHubService.Generate` RPC with the real model ID `gpt-image-2`. Reuse that client after every successful `dev` deployment to make one real text-to-image request through `modelhub.internal.dev:50053`.

The automated smoke is advisory: a functional GPT Image 2 failure publishes a sanitized warning but does not roll back the healthy ECS release or mark the pipeline execution failed.

## 2. Confirmed behavior

The delivery includes:

- a runnable Go text-to-image example in the `wgModelHub` repository;
- a shell wrapper suitable for both engineers and CodeBuild;
- one `1024x1024` `gpt-image-2` request after each successful `dev` release;
- protocol and media validation before the generated file is accepted;
- a VPC-attached post-deployment CodeBuild action;
- warning-only failure handling through the existing `wg-dev-cicd-failures` SNS topic;
- tests for the client, wrapper contract, pipeline order, networking, IAM, and non-blocking failure semantics.

The delivery excludes:

- public access, public DNS, a load balancer, TLS termination, or port `50054`;
- `authorization` metadata or API Key creation, because the smoke calls the internal `50053` listener;
- reference-image edit coverage in the live deployment smoke;
- automatic retries, to keep each successful deployment to at most one provider generation attempt;
- persistence of pipeline-generated images in S3, CodePipeline artifacts, or source control;
- changes to AppConfig, provider credentials, proxy behavior, the database, or migrations;
- treating image quality as a release gate. The smoke validates transport and protocol correctness, not subjective visual quality.

## 3. Current state

The deployed service already has the required runtime behavior:

- `gpt-image-2` is a real model constant and is routed to the OpenAI-compatible image provider;
- a text-only image request uses `/v1/images/generations` and defaults `1:1` to `1024x1024`;
- the internal gRPC service is discoverable as `modelhub.internal.dev:50053` only from approved private network paths;
- `Generate` returns image output as one server-streaming `GenerateEvent`, with the image response normalized by ModelHub;
- server and provider unit tests cover generations and reference-image edits;
- `ManagedRelease` already verifies DNS, TCP/gRPC readiness, two healthy application tasks, healthy AppConfig sidecars, and the released image digest.

The existing provider live test calls the provider adapter directly and depends on locally supplied provider credentials. It does not prove that the deployed ECS service, AppConfig routing, Cloud Map discovery, and gRPC protocol work together. The new smoke closes that gap.

## 4. Repository example client

### 4.1 Files and invocation

Add the Go command at:

```text
examples/gpt-image-2/main.go
```

Add the wrapper at:

```text
scripts/examples/gpt-image-2.sh
```

An engineer connected to the development VPC/VPN invokes it from the repository root:

```bash
./scripts/examples/gpt-image-2.sh \
  --address modelhub.internal.dev:50053 \
  --prompt "A small red paper boat floating on calm water" \
  --output ./gpt-image-2.png
```

The wrapper resolves the repository root, installs its traps before creating a permission-restricted temporary directory, builds the Go example there, forwards the arguments unchanged, and removes only that directory on exit. Build and client execution each run in a dedicated child process group. The background-launch boundary is explicit: while a child is becoming runnable and its PID/PGID are being registered, signal traps record the first pending conventional status; after both identifiers are registered, the wrapper immediately dispatches that status through the normal handler. On `INT`, `TERM`, `HUP`, or `EXIT`, the wrapper sends `TERM`, allows a short bounded grace period, escalates the child group to `KILL`, reaps the direct child, and then performs idempotent cleanup. Cleanup failure never replaces the conventional signal status. It does not delete the engineer's output image or signal the caller's process group.

Required flags are `--address`, `--prompt`, and `--output`. The client also accepts `--timeout`, defaulting to five minutes. The output parent directory must already exist, and the client refuses to overwrite an existing output file unless the caller supplies `--force`.

### 4.2 Request contract

The client always sends:

- `GenerateRequest.model = models.GPTImage2`;
- one `InputItem.message` with `ROLE_USER`;
- one text `ContentPart` containing the prompt;
- `OutputSpec.image.aspect_ratio = "1:1"`;
- `OutputSpec.image.image_size = "1024x1024"`.

The client does not accept a provider URL, provider name, credential, model override, or arbitrary metadata. It uses plaintext gRPC transport because this is the existing private listener contract. It sends no `authorization` metadata.

The gRPC connection and call use bounded contexts. The receive limit is `protocol.MaxRPCMessageBytes` so valid image responses are not rejected by the gRPC client's smaller default limit. `grpc.WithDisableRetry()` explicitly prevents a service config from enabling an application retry.

### 4.3 Response validation and file handling

The client consumes the stream through EOF and succeeds only when all of the following are true:

1. exactly one event has `final=true`;
2. exactly one image output item is present across the stream;
3. the image MIME is safe ASCII `image/<token>` with a non-empty token subtype (for example, `image/avif`), with no parameters, whitespace, or control characters;
4. the image uses inline bytes rather than a URI;
5. the byte content is non-empty and does not exceed `protocol.MaxMediaBytes`;
6. no event arrives after the unique final event;
7. the stream terminates normally.

Diagnostic text may accompany the image and is ignored. Video, tool-call, nil, absent/unknown oneof, and nil-image items fail the smoke whether they occur before or after the image. A blocked or diagnostic-only response without image bytes also fails.

The output is first written to a mode-`0600` temporary file in the requested destination directory. With `--force`, an atomic rename replaces the destination. Without `--force`, a same-directory hard link publishes the temporary inode atomically without clobbering a destination created concurrently, then unlinks the temporary name. Failure removes only the partial temporary file. Standard output contains only the MIME type, byte count, and output path. The prompt, image bytes, full response, provider details, and gRPC error message are never printed.

Errors are reduced to a stable local stage and, for RPC failures, the gRPC status code. This prevents upstream response bodies or sensitive details from reaching terminal or CodeBuild logs.

## 5. Automated smoke architecture

Extend the `wg-dev-modelhub` V2 pipeline to:

```text
Source -> Test -> Build -> Release/ManagedRelease -> Smoke/GPTImage2
```

The `Smoke` stage starts only after `ManagedRelease` has succeeded. Its action receives the same `Source` artifact as the rest of the execution and asserts that the resolved source revision is the 40-character commit supplied by the pipeline execution. It then builds and runs the repository wrapper, so manual and automated verification exercise the same client implementation.

Add a bounded optional service-manifest capability:

```yaml
postDeploySmoke:
  type: modelhub-gpt-image-2
  failureMode: warn
```

Only that enumerated type is supported. It does not permit a manifest to inject arbitrary shell commands. The type fixes the repository wrapper path, model, size, five-minute RPC timeout, lack of retry, and sanitized result contract. `failureMode: warn` is the only mode in this delivery.

The capability is valid only when the managed manifest is exactly the `modelhub` gRPC service with `runtime.cloudMapName: modelhub` and `runtime.port: 50053`. Runtime Zod validation rejects each mismatch at `postDeploySmoke`, and the construct defensively rejects any address or port other than `modelhub.internal.dev:50053` and `50053` before creating resources.

### 5.1 Network boundary

Create a dedicated smoke-test security group and attach the CodeBuild project to the existing private subnets. Add one ingress rule to the ModelHub service security group from that smoke security group on TCP `50053`.

The smoke project receives no public IP, load balancer, database ingress, AppConfig permission, provider credential, or ModelHub task role. Its only application interaction is the internal gRPC endpoint:

```text
modelhub.internal.dev:50053
```

The project may use its existing private-subnet egress path to obtain Go module dependencies. Provider traffic continues to originate only from the ModelHub ECS tasks.

### 5.2 Execution and cleanup

The build uses the exact fixed prompt `A centered red paper boat floating on calm blue water, simple studio illustration, no text`. This makes a valid image response easy to recognize while avoiding customer or business data. It creates a private temporary directory and asks for one `1024x1024` image.

The repository-owned build command starts an internal 510-second (8m30s) watchdog before `mktemp`, Base64 decoding, identity validation, `chmod`, or smoke dispatch. The construct supplies both the Base64 script and its SHA-256. The command creates a new mode-`0700` bootstrap directory, decodes to a file inside it, rejects missing/empty/whitespace/wrong content by checking non-empty bytes and the exact SHA-256, and keeps both coordination markers inside that directory. That single envelope covers bootstrap, wrapper build/module download, the five-minute dial/RPC context, response validation, cleanup, and the alert attempt while leaving margin before the CodeBuild project's ten-minute hard timeout. Cleanup is bounded to ten seconds and SNS publication to fifteen seconds.

Buildspec, smoke script, and repository wrapper install idempotent `INT`, `TERM`, `HUP`, and `EXIT` cleanup handlers. Each handler terminates and reaps its tracked child before removing only its own generated files, and it runs the relevant cleanup at most once. The wrapper additionally terminates the build/client process group after a short grace period so descendants that ignore `TERM` cannot outlive it. Whether the call succeeds or fails, the generated image, temporary binary, decoded script, summaries, coordination markers, and private temporary directories are deleted. The CodeBuild project defines no output artifact for the image. The source artifact remains the normal CodePipeline input and is unaffected.

There is no retry at the client, wrapper, or buildspec layer; the gRPC client explicitly disables retries even if a resolver supplies a retry service config. Each smoke execution therefore makes zero provider requests if setup fails, or exactly one provider generation attempt if the RPC reaches ModelHub.

## 6. Failure and alert semantics

A controlled smoke execution failure includes build-command bootstrap (`mktemp`, missing/empty/invalid Base64, decoded-script identity validation, `chmod`, or dispatch), source validation, client build, DNS, connection, RPC, watchdog/RPC timeout, stream/protocol validation, output-file handling, or cleanup failure after the repository-owned build command has started. The buildspec catches these failures, attempts exactly one warning, and exits successfully so the pipeline stays green and the ECS release remains active.

The warning is sent to the existing `wg-dev-cicd-failures` topic. Its JSON body contains only:

- CodeBuild event/build ID;
- AWS account and region;
- service name `modelhub`;
- source commit;
- status `warning`;
- source `gpt-image-2-smoke`;
- a stable non-sensitive failure category.

The allowed failure categories are `source-validation`, `client-build`, `connect`, `rpc`, `timeout`, `protocol`, `output`, and `cleanup`. No dynamic error text is copied into the warning.

It contains no prompt, media, AppConfig content, DSN, credential, provider response, gRPC message, or stack trace. `sourceCommit` is included only when `SOURCE_COMMIT` and `CODEBUILD_RESOLVED_SOURCE_VERSION` are both lowercase 40-character hashes and equal; otherwise it is the fixed value `invalid`. The SNS CLI is bounded to fifteen seconds. Private no-clobber attempt and alert-failure markers coordinate the decoded smoke script with the outer watchdog. If the attempt marker cannot be created, the child fails closed without SNS and the outer layer may attempt the warning once. If JSON construction or SNS publication fails, hangs, or is interrupted by the watchdog, the outer layer re-emits only the fixed `alert_publish_failed` marker and never exposes captured child stderr or performs a second publish attempt.

The `Smoke` stage has no rollback rule. CodeBuild-managed image and `runtime-versions` initialization happens before the repository-owned build command can install its handlers. A platform failure that prevents the container, managed Go runtime initialization, or the build command from starting cannot be caught by repository code and may mark the action failed; this narrow platform-startup boundary is not classified as a functional/setup failure. It still must not roll back the already healthy ECS release.

## 7. IAM and observability

The smoke CodeBuild role receives only the normal logging, VPC network-interface, and CodePipeline source-artifact permissions required for a pipeline project plus `sns:Publish` to the single existing warning topic. Report-group grants are explicitly disabled because this build emits no CodeBuild reports. It receives no ECS mutation, `iam:PassRole`, AppConfig, Secrets Manager, SSM parameter, RDS, ECR push, or provider-secret permission.

CloudWatch output is limited to:

- source commit;
- start/end timestamps and elapsed time;
- pass/fail status;
- stable failure category;
- MIME type and byte count on success.

The fixed prompt is not logged even though it is non-sensitive. Image bytes and raw responses are never logged. Application-side observability remains in Logfire project `server` and continues to exclude prompts and media.

## 8. Test strategy

### 8.1 `wgModelHub`

Use an in-process `bufconn` gRPC server to test the client without provider calls:

- a valid unique final event writes the exact bytes with mode `0600`;
- the request contains the fixed model, user text item, `1:1`, and `1024x1024`;
- the receive limit accepts a response larger than gRPC's default 4 MiB but within ModelHub limits;
- missing final, multiple finals, event after final, no image, multiple images, URI image, invalid MIME type, empty bytes, oversized bytes, and non-EOF stream errors fail;
- failure leaves no partial output;
- an existing output is protected unless `--force` is set;
- errors expose only stable local categories and gRPC codes.

Add shell-level contract tests for argument forwarding, repository-root independence, temporary binary cleanup, and preservation of the requested output.

No automated repository test makes a real provider call. The existing provider-unit and opt-in live tests remain unchanged.

### 8.2 `wgPlatformInfra`

CDK, static, and executable shell tests require:

- `Smoke` follows `Release` and contains one `GPTImage2` action;
- the action consumes the `Source` artifact from the same execution;
- the CodeBuild project is VPC-attached in private subnets;
- the dedicated smoke project reaches service port `50053` only through its own source-security-group rule, without broadening or removing existing approved caller and release-readiness rules;
- the role can publish only to `wg-dev-cicd-failures` and has no provider/config/database permissions;
- the manifest and construct reject wrong service name, protocol, Cloud Map name, address, and port with precise errors;
- the fixed wrapper, endpoint, model behavior, timeout, no-retry behavior, idempotent signal cleanup, and sanitized warning payload are present;
- bootstrap failure, wrapper hang, SNS hang/failure (including watchdog interruption during SNS), and `INT`/`TERM`/`HUP` paths execute with bounded completion, exact-once cleanup, no dynamic stderr, and at most one warning attempt;
- safe `image/<token>` MIME values such as `image/avif` succeed while empty subtypes, whitespace, and control characters fail;
- a functional client failure is converted to a successful CodeBuild exit after the warning attempt;
- the Smoke stage has no rollback configuration and emits no image artifact;
- services without `postDeploySmoke` are unchanged.

## 9. Rollout and live verification

Implementation and activation use this order:

1. implement and verify the `wgModelHub` example client and wrapper;
2. after push approval, push the client commit to `dev` and let the current pipeline deploy it normally;
3. implement and verify the infrastructure smoke project and stage;
4. after deployment approval, deploy the infrastructure change to `wg-dev-modelhub-service`;
5. start one pipeline execution for the exact current `dev` commit to exercise the new stage;
6. verify `ManagedRelease` succeeds, `GPTImage2` reports success, two ECS tasks remain healthy, and no image artifact is retained;
7. verify later `dev` pushes trigger the same smoke automatically.

Step 5 intentionally incurs one real `gpt-image-2` generation attempt. It requires the same explicit production-like deployment approval used for the infrastructure activation.

If the smoke infrastructure must be rolled back, remove the `Smoke` stage and its dedicated CodeBuild/security-group resources. Do not revert or restart the healthy ModelHub service solely because the advisory smoke failed.

## 10. Acceptance criteria

The work is complete when:

- the documented manual command creates a valid local image through the internal deployed gRPC service;
- the client validates the ModelHub stream and never logs sensitive request or response content;
- every successful `dev` release automatically starts one `gpt-image-2` text-to-image smoke;
- a functional smoke failure produces a sanitized SNS warning while leaving the pipeline execution successful and the ECS release untouched;
- no pipeline image is retained in artifacts or logs;
- the pipeline still has no public ModelHub access and the running service remains at two healthy internal replicas.
