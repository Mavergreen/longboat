#!/bin/sh
# platform: host-agnostic
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-progress.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0
CR="$(printf '\r')"

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

run() {
  sh -c '. "$1/src/progress.sh"; shift; lb_progress_init 3; "$@"' sh "$root" "$@" >"$T/out" 2>"$T/err"
}

cat > "$T/installer-109" <<'EOF'
installer: Package name is
installer: Installing at base path /
installer:PHASE:Preparing for installation…
installer:STATUS:
installer:%2.229250
installer:PHASE:Validating packages…
installer:%97.750000
installer:STATUS:Running installer actions…
installer:%100.000000
installer:PHASE:The software was successfully installed.
installer: The install was successful.
EOF

no_cr() {
  ! grep -q "$CR" "$T/err" || { echo "a log must not hold carriage returns" >&2; return 1; }
}

test_run_success_lines() {
  rc=0
  run lb_step_run 2 'copying the SDK' sh -c 'echo noise >&2' || rc=$?
  [ "$rc" -eq 0 ] || { echo "status $rc, want 0" >&2; return 1; }
  [ "$(wc -l < "$T/err" | tr -d ' ')" -eq 2 ] || { echo "want two lines, got: $(cat "$T/err")" >&2; return 1; }
  [ "$(sed -n 1p "$T/err")" = '[2/3] copying the SDK...' ] || { echo "start line: $(sed -n 1p "$T/err")" >&2; return 1; }
  sed -n 2p "$T/err" | grep -Eq '^\[2/3\] copying the SDK: done \([0-9]+:[0-5][0-9]\)$' \
    || { echo "finish line: $(sed -n 2p "$T/err")" >&2; return 1; }
  ! grep -q noise "$T/err" || { echo "a step that succeeds must not show its output" >&2; return 1; }
  no_cr
}

test_run_failure_shows_held_output() {
  rc=0
  run lb_step_run 2 'copying the SDK' sh -c 'echo boom >&2; exit 7' || rc=$?
  [ "$rc" -eq 7 ] || { echo "status $rc, want 7" >&2; return 1; }
  sed -n 2p "$T/err" | grep -q '^\[2/3\] copying the SDK: failed (' \
    || { echo "want the failed line second, got: $(cat "$T/err")" >&2; return 1; }
  [ "$(sed -n 3p "$T/err")" = boom ] || { echo "want the held output after the failed line, got: $(cat "$T/err")" >&2; return 1; }
  no_cr
}

test_run_holds_stdout_too() {
  run lb_step_run 2 'copying the SDK' sh -c 'echo out' || return 1
  [ ! -s "$T/out" ] || { echo "the caller's stdout got: $(cat "$T/out")" >&2; return 1; }
  ! grep -q out "$T/err" || { echo "a step that succeeds must not show its stdout either" >&2; return 1; }
}

test_run_leaves_no_temp_files() {
  mkdir -p "$T/tmp"
  (TMPDIR="$T/tmp"; export TMPDIR; run lb_step_run 2 'copying the SDK' true) || return 1
  (TMPDIR="$T/tmp"; export TMPDIR; run lb_step_run 2 'copying the SDK' sh -c 'echo held; exit 1') && return 1
  [ -z "$(ls -A "$T/tmp")" ] || { echo "left behind: $(ls -A "$T/tmp")" >&2; return 1; }
}

test_show_passes_through() {
  run lb_step_show 1 'fetching the 10.9 SDK' sh -c 'echo path; echo info >&2' || return 1
  [ "$(cat "$T/out")" = path ] || { echo "stdout: $(cat "$T/out")" >&2; return 1; }
  [ "$(sed -n 1p "$T/err")" = '[1/3] fetching the 10.9 SDK...' ] \
    && [ "$(sed -n 2p "$T/err")" = info ] \
    && sed -n 3p "$T/err" | grep -Eq '^\[1/3\] fetching the 10.9 SDK: done \([0-9]+:[0-5][0-9]\)$' \
    && [ "$(wc -l < "$T/err" | tr -d ' ')" -eq 3 ] \
    || { echo "stderr: $(cat "$T/err")" >&2; return 1; }
  no_cr
}

test_show_returns_status() {
  rc=0
  run lb_step_show 1 'fetching the 10.9 SDK' sh -c 'exit 4' || rc=$?
  [ "$rc" -eq 4 ] && sed -n 2p "$T/err" | grep -q '^\[1/3\] fetching the 10.9 SDK: failed (' \
    || { echo "status $rc, stderr: $(cat "$T/err")" >&2; return 1; }
}

test_note() {
  run lb_step_note 1 'fetching the 10.9 SDK' cached || return 1
  [ "$(cat "$T/err")" = '[1/3] fetching the 10.9 SDK: cached' ] && [ ! -s "$T/out" ] \
    || { echo "stderr: $(cat "$T/err")" >&2; return 1; }
}

test_start_then_end_done() {
  sh -c '. "$1/src/progress.sh"; lb_progress_init 3; lb_step_start 2 installing; lb_step_end 2 installing 0' \
    sh "$root" 2>"$T/err" || return 1
  [ "$(sed -n 1p "$T/err")" = '[2/3] installing...' ] \
    && sed -n 2p "$T/err" | grep -Eq '^\[2/3\] installing: done \([0-9]+:[0-5][0-9]\)$' \
    && [ "$(wc -l < "$T/err" | tr -d ' ')" -eq 2 ] \
    || { echo "stderr: $(cat "$T/err")" >&2; return 1; }
  no_cr
}

test_start_then_end_failed() {
  sh -c '. "$1/src/progress.sh"; lb_progress_init 3; lb_step_start 3 installing; lb_step_end 3 installing 3' \
    sh "$root" 2>"$T/err" || return 1
  [ "$(sed -n 1p "$T/err")" = '[3/3] installing...' ] \
    && sed -n 2p "$T/err" | grep -Eq '^\[3/3\] installing: failed \([0-9]+:[0-5][0-9]\)$' \
    || { echo "stderr: $(cat "$T/err")" >&2; return 1; }
}

# spec: src/progress.sh lb_step_run -- only a signal makes a step "interrupted"; a command that exits
#       130 by itself failed, like any other non-zero status
test_run_exit_130_is_failed() {
  rc=0
  run lb_step_run 1 x sh -c 'exit 130' || rc=$?
  [ "$rc" -eq 130 ] && sed -n 2p "$T/err" | grep -Eq '^\[1/3\] x: failed \([0-9]+:[0-5][0-9]\)$' \
    || { echo "rc $rc, stderr: $(cat "$T/err")" >&2; return 1; }
}

test_installer_bar_log_passthrough() {
  run lb_installer_bar 5 installing < "$T/installer-109" || return 1
  cat "$T/out" "$T/err" > "$T/both"
  cmp -s "$T/installer-109" "$T/both" || { echo "got: $(cat "$T/both")" >&2; return 1; }
}

test_installer_bar_passes_failure_through() {
  printf '%s\n' 'installer:%40.0' 'installer: The install failed (The Installer encountered an error...)' > "$T/failing"
  run lb_installer_bar 5 installing < "$T/failing" || return 1
  grep -qF 'installer: The install failed (The Installer encountered an error...)' "$T/err" \
    || { echo "got: $(cat "$T/err")" >&2; return 1; }
}

for t in test_run_success_lines test_run_failure_shows_held_output test_run_holds_stdout_too \
    test_run_leaves_no_temp_files test_show_passes_through test_show_returns_status test_note \
    test_start_then_end_done test_start_then_end_failed test_run_exit_130_is_failed \
    test_installer_bar_log_passthrough test_installer_bar_passes_failure_through; do
  check "$t"
done
exit "$fail"
