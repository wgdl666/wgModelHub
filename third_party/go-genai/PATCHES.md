# Local patch to google.golang.org/genai v1.43.0

Upstream: https://github.com/googleapis/go-genai/tree/v1.43.0

The unmodified module source and Apache-2.0 LICENSE are retained here so CI and
container builds use the same code without a developer-local replacement.

Local implementation changes are limited to the two GenerateContent entry points
in `models.go` and the new `generate_content_media.go` helper. Tests and benchmarks
are in `generate_content_media_test.go`.

Non-empty `contents[].parts[].inlineData.data` and system-instruction inline data
are omitted from temporary copies during the SDK's JSON-to-map round trip, then
restored as references to the original bytes. Existing converters, custom JSON
rules and the final buffered JSON encoding are preserved. Other SDK APIs and
nested function-response media retain their upstream behavior.

When a request configures `HTTPOptions.ExtrasRequestProvider`, the entire request
uses the original round trip, preserving Base64-string callback values and their
snapshot before callback execution. Normal input buffers must remain read-only
while the SDK constructs the request. Concurrently modifying input bytes is not
supported; the optimization delays the inline-data read until final encoding.

Run from the ModelHub repository root:

```sh
go test google.golang.org/genai -mode=unit -run '^TestMediaPatch'
go test -race google.golang.org/genai -mode=unit -run '^TestMediaPatch'
go test google.golang.org/genai -mode=unit -run '^$' -bench '^BenchmarkMediaPatch$' -benchmem
```

Docker builds run these SDK regression tests explicitly because `go test ./...`
does not recurse into this nested module. To upgrade the SDK, reapply and validate
this patch against the new upstream source; retain the license and changed-file
notice. This patch has not been submitted or accepted upstream.
