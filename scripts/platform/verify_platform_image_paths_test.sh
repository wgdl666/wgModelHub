#!/usr/bin/env bash
set -euo pipefail

test_root="$(mktemp -d)"
cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT

mkdir -p "$test_root/etc/ssl/certs" "$test_root/etc/ssl1.1" "$test_root/app"
touch "$test_root/etc/ssl/cert.pem"
touch "$test_root/etc/ssl/certs/ca.pem"
touch "$test_root/etc/ssl1.1/cert.pem"

scripts/platform/verify_platform_image.sh --verify-private-key-paths "$test_root"

if ! grep -Fq 'apk add --no-cache --upgrade ca-certificates "openssl>=3.5.8-r0"' Dockerfile; then
  echo 'Dockerfile does not explicitly upgrade OpenSSL to the required floor' >&2
  exit 1
fi

verifier='scripts/platform/verify_platform_image.sh'
helper_body="$(awk '
  /^require_openssl_floor\(\) \{$/ { helper = 1 }
  helper { print }
  helper && /^\}$/ { exit }
' "$verifier")"
if [[ -z "$helper_body" ]]; then
  echo 'image verifier does not define the OpenSSL floor helper' >&2
  exit 1
fi

for required_contract in \
  'docker run --rm --network none --read-only --cap-drop ALL' \
  '--security-opt no-new-privileges --entrypoint /bin/sh' \
  "/sbin/apk info --exists 'openssl>=3.5.8-r0'"; do
  if [[ "$helper_body" != *"$required_contract"* ]]; then
    echo "OpenSSL floor helper does not enforce the required contract: $required_contract" >&2
    exit 1
  fi
done

helper_calls="$(awk '
  /^require_openssl_floor\(\) \{$/ { helper = 1; next }
  helper && /^\}$/ { helper = 0; next }
  !helper { print }
' "$verifier")"
for required_call in \
  "require_openssl_floor 'production image' \"\$PLATFORM_IMAGE\"" \
  "require_openssl_floor 'migration image' \"\$MIGRATION_IMAGE\""; do
  if ! printf '%s\n' "$helper_calls" | grep -Fxq -- "$required_call"; then
    echo "image verifier does not invoke the OpenSSL floor helper: $required_call" >&2
    exit 1
  fi
done

touch "$test_root/app/private.pem"
if scripts/platform/verify_platform_image.sh --verify-private-key-paths "$test_root" >/dev/null 2>&1; then
  echo "private PEM outside the system CA paths was accepted" >&2
  exit 1
fi
