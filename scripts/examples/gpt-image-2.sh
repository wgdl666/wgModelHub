#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
go_bin=${WG_MODELHUB_GO_BIN:-go}
umask 077
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wg-modelhub-gpt-image-2.XXXXXXXX")
cleanup() { rm -rf -- "$tmp_dir"; }
trap cleanup EXIT

if ! (cd -- "$repo_root" && "$go_bin" build -o "$tmp_dir/gpt-image-2" ./examples/gpt-image-2); then
  printf '%s\n' 'gpt-image-2 client build failed' >&2
  exit 70
fi

"$tmp_dir/gpt-image-2" "$@"
