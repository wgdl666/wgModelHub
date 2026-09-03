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
cleanup() {
  if ((cleanup_done)); then
    return
  fi
  cleanup_done=1
  [[ -z "$tmp_dir" ]] || rm -rf -- "$tmp_dir"
}
stop_active_child() {
  if [[ -z "$active_pid" ]]; then
    return
  fi
  local pid=$active_pid
  local pgid=$active_pgid
  local attempt
  active_pid=
  active_pgid=
  kill -TERM -- "-$pgid" 2>/dev/null || true
  for attempt in {1..10}; do
    if ! kill -0 -- "-$pgid" 2>/dev/null; then
      break
    fi
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
trap 'handle_exit $?' EXIT
trap 'handle_signal 130' INT
trap 'handle_signal 143' TERM
trap 'handle_signal 129' HUP

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wg-modelhub-gpt-image-2.XXXXXXXX")
set -m
(cd -- "$repo_root" && exec "$go_bin" build -o "$tmp_dir/gpt-image-2" ./examples/gpt-image-2) >/dev/null 2>&1 &
active_pid=$!
active_pgid=$active_pid
set +m
build_status=0
wait "$active_pid" || build_status=$?
active_pid=
if ((build_status != 0)); then
  printf '%s\n' 'gpt-image-2 client build failed' >&2
  exit 70
fi

set -m
"$tmp_dir/gpt-image-2" "$@" &
active_pid=$!
active_pgid=$active_pid
set +m
client_status=0
wait "$active_pid" || client_status=$?
active_pid=
exit "$client_status"
