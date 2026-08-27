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

touch "$test_root/app/private.pem"
if scripts/platform/verify_platform_image.sh --verify-private-key-paths "$test_root" >/dev/null 2>&1; then
  echo "private PEM outside the system CA paths was accepted" >&2
  exit 1
fi
