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
launch_in_progress=0
pending_signal_status=''
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

queue_or_handle_signal() {
  local status=$1
  if ((launch_in_progress)); then
    if [[ -z "$pending_signal_status" ]]; then
      pending_signal_status=$status
    fi
    return
  fi
  handle_signal "$status"
}

begin_child_launch() {
  pending_signal_status=''
  launch_in_progress=1
}

finish_child_launch() {
  local status
  launch_in_progress=0
  if [[ -n "$pending_signal_status" ]]; then
    status=$pending_signal_status
    pending_signal_status=''
    handle_signal "$status"
  fi
}

trap 'handle_exit $?' EXIT
trap 'queue_or_handle_signal 130' INT
trap 'queue_or_handle_signal 143' TERM
trap 'queue_or_handle_signal 129' HUP

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wg-modelhub-gpt-image-2.XXXXXXXX")
set -m
begin_child_launch
(cd -- "$repo_root" && exec "$go_bin" build -o "$tmp_dir/gpt-image-2" ./examples/gpt-image-2) >/dev/null 2>&1 &
active_pid=$!
active_pgid=$active_pid
finish_child_launch
set +m
build_status=0
wait "$active_pid" || build_status=$?
active_pid=
active_pgid=
if ((build_status != 0)); then
  printf '%s\n' 'gpt-image-2 client build failed' >&2
  exit 70
fi

set -m
begin_child_launch
"$tmp_dir/gpt-image-2" "$@" &
active_pid=$!
active_pgid=$active_pid
finish_child_launch
set +m
client_status=0
wait "$active_pid" || client_status=$?
active_pid=
active_pgid=
exit "$client_status"
