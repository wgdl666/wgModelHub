# Test-only DashScope embedding HTTP timeout

Base: exact d090bd581b8a33707c17473754675c31efbe8aa0. Scope: config provider field, its validation/resource identity, factory binding, provider HTTP client; no protocol/model/body/image/prompt/other provider change. Full baseline go test ./... passed.

Evidence: test trace8e56e1cf5bd802263a0f470ac4592f81 entered 阿里云百炼_dashscope_embedding and failed after20022ms waiting for HTTP headers, matching the fixed20s provider timeout. The owned garment was read before the RPC. A longer diagnostic CLI deadline cannot override provider HTTP client.Timeout. There is no existing dashscope_embedding timeout knob in exactd090.

Small design: optional `dashscope_embedding.http_timeout` accepts a Go duration string. Omitted/blank means existing20s; malformed/nonpositive values reject startup/updates. This is a connection resource and therefore participates in existing DeepEqual restart detection; hot updates must retain the old complete config, cold restart binds the override. Parent-only protected test v12 uses120s alongside the same G7 model at its private endpoint. Dev config stays omitted/20s.

RED: parse real YAML with invalid values (currently ignored), change only timeout and expect restart rejection (currently ignored), and use a real local HTTP server through factory/provider to show 50ms header/body deadlines (currently returns after250ms). GREEN: minimal field/parser/default helper, pass normalized timeout to existing provider constructor, use the same traced http.Client.Timeout. Tests also prove caller cancellation wins before a larger provider timeout and default actual transport boundary retains20s. No real model calls.

Delivery: focused tests, full suite/race/build/vet, exact base/commit/diff hashes, private config sanitized diff+validation, Linux binaries, image-only source/build request candidate for review. No remote upload/build/deploy/config/SG write/inference.

## Verification

The original source failed the new semantic tests: invalid duration values were ignored; the resource diff was empty; real local HTTP header/body waits returned success after250ms despite a50ms setting; the actual outgoing transport deadline remained20s with120s configured. Caller cancellation and the omitted20s behavior already passed as characterization checks.

After the patch, all of those tests passed. The configured timeout cancels both the real upstream request and response-body wait, and caller cancellation still wins with120s configured. Actual request payload/model/1024-dimensional parameters/data URI/endpoint/auth headers remain unchanged at the transport boundary. Expected local-test logs include the refused hot-update error and caller-canceled warning.

`go test ./...`, focused `go test -race ./config ./internal/infra/factory ./internal/infra/dashscopeembed ./schema`, and `go vet ./...` passed. All tests use synthetic local fixtures; no real provider, database, S3, or model calls occur. The full suite also passed before edits.

The test-only protected v12 candidate is based on actual v11, with exactly two fields changed: private G7 base URL and embedding http_timeout120s. The production source diff does not change G7 provider code or any other provider, protocol, model constant, prompt, image transformation, dimension, retry or ledger behavior. The runtime resource comparator already includes the new nested timeout field; no special hot-reload exception was added. Oldd090 ignores this unknown field, so a new binary plus cold restart is necessary for120s to take effect.
