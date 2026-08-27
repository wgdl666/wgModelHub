#!/usr/bin/env bash
set -euo pipefail

fail() {
  printf 'verify_platform_image: %s\n' "$*" >&2
  exit 1
}

inspect() {
  docker image inspect --format "$1" "$PLATFORM_IMAGE"
}

migration_inspect() {
  docker image inspect --format "$1" "$MIGRATION_IMAGE"
}

require_openssl_floor() {
  local description="$1"
  local image="$2"
  docker run --rm --network none --read-only --cap-drop ALL \
    --security-opt no-new-privileges --entrypoint /bin/sh "$image" \
    -c "/sbin/apk info --exists 'openssl>=3.5.8-r0'" \
    >/dev/null 2>&1 || fail "$description OpenSSL must be at least 3.5.8-r0"
}

require_equal() {
  local description="$1"
  local actual="$2"
  local expected="$3"
  [[ "$actual" == "$expected" ]] || fail "$description: got $actual, want $expected"
}

reject_path() {
  local description="$1"
  shift
  local match
  match="$(find "$rootfs" "$@" -print -quit)" || fail "could not scan $description"
  [[ -z "$match" ]] || fail "$description: ${match#"$rootfs"}"
}

reject_private_key_paths() {
  reject_path 'private key path' \( -iname '*.key' -o -iname '*.pem' \) \
    ! -path "$rootfs/etc/ssl/certs/*" \
    ! -path "$rootfs/etc/ssl/cert.pem" \
    ! -path "$rootfs/etc/ssl1.1/cert.pem"
}

scan_credentials() {
  local pattern status
  pattern='(?:(?<![A-Za-z0-9])(?:AKIA|ASIA)[0-9A-Z]{16}(?![A-Za-z0-9])|(?m:^-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----)|(?:ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|sk_(?:live|test)_[A-Za-z0-9_=-]{16,}|AIza[A-Za-z0-9_-]{35})|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|(?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis(?:s)?|amqps?)://\S+:\S{8,}@)'
  if command -v rg >/dev/null 2>&1; then
    set +e
    rg --pcre2 --multiline --hidden --no-ignore -a -q -e "$pattern" "$rootfs" >/dev/null
    status=$?
    set -e
  elif command -v python3 >/dev/null 2>&1; then
    set +e
    python3 - "$rootfs" "$pattern" <<'PY'
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
pattern = re.compile(sys.argv[2], re.MULTILINE)
try:
    for path in root.rglob("*"):
        if path.is_file() and pattern.search(path.read_bytes().decode("utf-8", "surrogateescape")):
            raise SystemExit(0)
except OSError as exc:
    print(f"credential scan failed: {exc}", file=sys.stderr)
    raise SystemExit(2)
raise SystemExit(1)
PY
    status=$?
    set -e
  else
    fail 'credential scan requires rg or python3'
  fi
  case "$status" in
    0) fail 'image contains a high-confidence credential, private key, token, or credentialed DSN' ;;
    1) return 0 ;;
    *) fail "credential scan failed with status $status" ;;
  esac
}

if [[ "${1:-}" == "--verify-private-key-paths" ]]; then
  [[ "$#" == "2" && -d "$2" ]] || fail 'self-test root must be an existing directory'
  rootfs="$2"
  reject_private_key_paths
  exit 0
fi

: "${PLATFORM_IMAGE:?set PLATFORM_IMAGE to the production image}"
: "${EXPECTED_REVISION:?set EXPECTED_REVISION to the exact source revision}"

[[ "$EXPECTED_REVISION" =~ ^[0-9a-f]{40}$ ]] \
  || fail 'EXPECTED_REVISION must be a full lowercase Git SHA'
[[ -n "${MIGRATION_IMAGE:-}" ]] || fail 'set MIGRATION_IMAGE to the migration image'

require_equal 'image OS' "$(inspect '{{.Os}}')" 'linux'
require_equal 'image architecture' "$(inspect '{{.Architecture}}')" 'amd64'
require_equal 'image title' \
  "$(inspect '{{ index .Config.Labels "org.opencontainers.image.title" }}')" \
  'wg-model-hub'
require_equal 'image source' \
  "$(inspect '{{ index .Config.Labels "org.opencontainers.image.source" }}')" \
  'https://github.com/wgdl666/wgModelHub'
require_equal 'image revision' \
  "$(inspect '{{ index .Config.Labels "org.opencontainers.image.revision" }}')" \
  "$EXPECTED_REVISION"
require_equal 'container user' "$(inspect '{{.Config.User}}')" '10001:10001'
require_equal 'entrypoint' \
  "$(inspect '{{json .Config.Entrypoint}}')" \
  '["/usr/local/bin/wg-model-hub"]'
require_equal 'exposed ports' \
  "$(inspect '{{json .Config.ExposedPorts}}')" \
  '{"50053/tcp":{}}'

require_equal 'migration image OS' "$(migration_inspect '{{.Os}}')" 'linux'
require_equal 'migration image architecture' "$(migration_inspect '{{.Architecture}}')" 'amd64'
require_equal 'migration image revision' \
  "$(migration_inspect '{{ index .Config.Labels "org.opencontainers.image.revision" }}')" \
  "$EXPECTED_REVISION"
require_equal 'migration container user' "$(migration_inspect '{{.Config.User}}')" '10001:10001'
require_equal 'migration entrypoint' \
  "$(migration_inspect '{{json .Config.Entrypoint}}')" \
  '["/usr/local/bin/wg-model-hub-migrate"]'

require_openssl_floor 'production image' "$PLATFORM_IMAGE"
require_openssl_floor 'migration image' "$MIGRATION_IMAGE"

docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --entrypoint /bin/sh "$MIGRATION_IMAGE" \
  -c 'test -x /usr/local/bin/wg-model-hub-migrate' \
  >/dev/null 2>&1 || fail 'migration binary is not executable by the configured user'

container=''
rootfs="$(mktemp -d)"
cleanup() {
  if [[ -n "$container" ]]; then
    docker rm "$container" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$rootfs"
}
trap cleanup EXIT

container="$(docker create "$PLATFORM_IMAGE")"
docker export "$container" | tar -x -C "$rootfs"

for binary in wg-model-hub wg-model-hub-healthcheck; do
  [[ -f "$rootfs/usr/local/bin/$binary" && ! -L "$rootfs/usr/local/bin/$binary" ]] \
    || fail "$binary must be a regular file"
done
[[ ! -e "$rootfs/usr/local/bin/wg-model-hub-migrate" ]] \
  || fail 'production image must not contain the migration binary'
docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --entrypoint /bin/sh "$PLATFORM_IMAGE" \
  -c 'test -x /usr/local/bin/wg-model-hub && test -x /usr/local/bin/wg-model-hub-healthcheck' \
  >/dev/null 2>&1 || fail 'runtime binaries are not executable by the configured user'

reject_path 'Git metadata' -name '.git'
reject_path 'local configuration' \( -name '.config.yaml' -o -name 'bootstrap.json' -o -name 'example.modelHub.yaml' -o -name '.env' -o -name '.env.*' \)
reject_path 'SQL migration file' -type f -name '*.sql'
reject_path 'Go source file' -type f -name '*.go'
reject_private_key_paths
scan_credentials

printf 'verify_platform_image: %s satisfies the ModelHub production contract\n' "$PLATFORM_IMAGE"
