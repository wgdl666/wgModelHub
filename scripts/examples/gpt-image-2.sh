#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(cd -- "$script_dir/../.." && pwd -P)
go_bin=${WG_MODELHUB_GO_BIN:-go}
umask 077
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wg-modelhub-gpt-image-2.XXXXXXXX")
cleanup_done=0
active_pid=
cleanup() {
  if ((cleanup_done)); then
    return
  fi
  cleanup_done=1
  rm -rf -- "$tmp_dir"
}
stop_active_child() {
  if [[ -z "$active_pid" ]]; then
    return
  fi
  local pid=$active_pid
  active_pid=
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}
handle_signal() {
  local status=$1
  stop_active_child
  cleanup
  trap - EXIT
  exit "$status"
}
trap cleanup EXIT
trap 'handle_signal 130' INT
trap 'handle_signal 143' TERM
trap 'handle_signal 129' HUP

(cd -- "$repo_root" && exec "$go_bin" build -o "$tmp_dir/gpt-image-2" ./examples/gpt-image-2) >/dev/null 2>&1 &
active_pid=$!
build_status=0
wait "$active_pid" || build_status=$?
active_pid=
if ((build_status != 0)); then
  printf '%s\n' 'gpt-image-2 client build failed' >&2
  exit 70
fi

"$tmp_dir/gpt-image-2" "$@" &
active_pid=$!
client_status=0
wait "$active_pid" || client_status=$?
active_pid=
exit "$client_status"
