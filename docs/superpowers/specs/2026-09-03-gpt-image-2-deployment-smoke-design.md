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

The wrapper resolves the repository root, builds the Go example into a permission-restricted temporary directory, forwards the arguments unchanged, and removes only its temporary binary on exit. It does not delete the engineer's output image.

Required flags are `--address`, `--prompt`, and `--output`. The client also accepts `--timeout`, defaulting to five minutes. The output parent directory must already exist, and the client refuses to overwrite an existing output file unless the caller supplies `--force`.

### 4.2 Request contract

The client always sends:

- `GenerateRequest.model = models.GPTImage2`;
- one `InputItem.message` with `ROLE_USER`;
- one text `ContentPart` containing the prompt;
- `OutputSpec.image.aspect_ratio = "1:1"`;
- `OutputSpec.image.image_size = "1024x1024"`.

The client does not accept a provider URL, provider name, credential, model override, or arbitrary metadata. It uses plaintext gRPC transport because this is the existing private listener contract. It sends no `authorization` metadata.

The gRPC connection and call use bounded contexts. The receive limit is `protocol.MaxRPCMessageBytes` so valid image responses are not rejected by the gRPC client's smaller default limit.

### 4.3 Response validation and file handling

The client consumes the stream through EOF and succeeds only when all of the following are true:

1. exactly one event has `final=true`;
2. exactly one image output item is present across the stream;
3. the image has a non-empty `image/*` MIME type;
4. the image uses inline bytes rather than a URI;
5. the byte content is non-empty and does not exceed `protocol.MaxMediaBytes`;
6. no event arrives after the unique final event;
7. the stream terminates normally.

Diagnostic text may accompany the image and is ignored. A blocked or diagnostic-only response without image bytes fails the smoke.

The output is first written to a mode-`0600` temporary file in the requested destination directory and then atomically renamed to the requested path. Failure removes the partial temporary file. Standard output contains only the MIME type, byte count, and output path. The prompt, image bytes, full response, provider details, and gRPC error message are never printed.

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

### 5.1 Network boundary

Create a dedicated smoke-test security group and attach the CodeBuild project to the existing private subnets. Add one ingress rule to the ModelHub service security group from that smoke security group on TCP `50053`.

The smoke project receives no public IP, load balancer, database ingress, AppConfig permission, provider credential, or ModelHub task role. Its only application interaction is the internal gRPC endpoint:

```text
modelhub.internal.dev:50053
```

The project may use its existing private-subnet egress path to obtain Go module dependencies. Provider traffic continues to originate only from the ModelHub ECS tasks.

### 5.2 Execution and cleanup

The build uses the exact fixed prompt `A centered red paper boat floating on calm blue water, simple studio illustration, no text`. This makes a valid image response easy to recognize while avoiding customer or business data. It creates a private temporary directory, asks for one `1024x1024` image, and installs an exit trap before invoking the client.

Whether the call succeeds or fails, the trap deletes the generated image, temporary binary, and temporary directory. The CodeBuild project defines no output artifact for the image. The source artifact remains the normal CodePipeline input and is unaffected.

There is no retry at the client, wrapper, or buildspec layer. Each smoke execution therefore makes zero provider requests if setup fails, or exactly one provider generation attempt if the RPC reaches ModelHub.

## 6. Failure and alert semantics

A smoke execution failure includes source validation, client build, DNS, connection, RPC, timeout, stream/protocol validation, output-file handling, or cleanup failure after the CodeBuild container has started. The buildspec catches these failures, publishes one warning, and exits successfully so the pipeline stays green and the ECS release remains active.

The warning is sent to the existing `wg-dev-cicd-failures` topic. Its JSON body contains only:

- CodeBuild event/build ID;
- AWS account and region;
- service name `modelhub`;
- source commit;
- status `warning`;
- source `gpt-image-2-smoke`;
- a stable non-sensitive failure category.

The allowed failure categories are `source-validation`, `client-build`, `connect`, `rpc`, `timeout`, `protocol`, `output`, and `cleanup`. No dynamic error text is copied into the warning.

It contains no prompt, media, AppConfig content, DSN, credential, provider response, gRPC message, or stack trace. If SNS publication itself fails, the build logs a fixed `alert_publish_failed` marker and still exits successfully.

The `Smoke` stage has no rollback rule. A CodeBuild platform failure that prevents the build container from starting cannot be caught by the buildspec and may mark the pipeline action failed, but it still must not roll back the already healthy ECS release.

## 7. IAM and observability

The smoke CodeBuild role receives only the normal logging and CodePipeline artifact permissions created for a pipeline project plus `sns:Publish` to the single existing warning topic. It receives no ECS mutation, `iam:PassRole`, AppConfig, Secrets Manager, SSM parameter, RDS, ECR push, or provider-secret permission.

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

CDK and static buildspec tests require:

- `Smoke` follows `Release` and contains one `GPTImage2` action;
- the action consumes the `Source` artifact from the same execution;
- the CodeBuild project is VPC-attached in private subnets;
- the dedicated smoke project reaches service port `50053` only through its own source-security-group rule, without broadening or removing existing approved caller and release-readiness rules;
- the role can publish only to `wg-dev-cicd-failures` and has no provider/config/database permissions;
- the fixed wrapper, endpoint, model behavior, timeout, no-retry behavior, cleanup trap, and sanitized warning payload are present;
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
