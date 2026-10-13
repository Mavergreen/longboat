#!/bin/sh
# platform: macOS-only -- BSD script(1) takes the command after the transcript file, which gives each case a pseudo-terminal
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-progress-tty.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0
CR="$(printf '\r')"

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

at_tty() {
  script -q /dev/null sh -c '. "$1/src/progress.sh"; lb_progress_init "$2"; eval "$3"' sh "$root" "$1" "$2" \
    </dev/null >"$T/tty" 2>&1
  tr '\r' '\n' < "$T/tty" > "$T/segs"
}

last_seg() {
  awk 'NF { l = $0 } END { print l }' "$T/segs"
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

test_clock_ticks() {
  at_tty 1 "lb_step_run 1 'building the SDK pkg' sleep 3" || return 1
  grep -q "$CR" "$T/tty" || { echo "no carriage return: the clock never redrew" >&2; return 1; }
  n="$(grep -o '(0:0[0-9])' "$T/segs" | sort -u | wc -l | tr -d ' ')"
  [ "$n" -ge 2 ] || { echo "want at least two clock values, got: $(cat "$T/segs")" >&2; return 1; }
  case "$(last_seg)" in
    '[1/1] building the SDK pkg: done (0:0'*) ;;
    *) echo "want the done line last, got: $(last_seg)" >&2; return 1 ;;
  esac
}

test_clock_line_fits_79_columns() {
  label=0123456789012345678901234567890123456789012345678901234567890123456789
  at_tty 1 "lb_step_run 1 '$label' sleep 2; lb_step_note 1 '$label' cached; lb_step_run 1 '$label' false" || :
  long="$(awk 'length($0) > 79' "$T/segs")"
  [ -z "$long" ] || { echo "longer than 79 columns: $long" >&2; return 1; }
  grep -q '^\[1/1\] 0123.*\.\.\. (0:01)$' "$T/segs" || { echo "the clock never drew: $(cat "$T/segs")" >&2; return 1; }
}

test_installer_bar_redraws() {
  at_tty 5 "lb_installer_bar 5 installing < '$T/installer-109'" || return 1
  grep -qF '[5/5] installing  97%' "$T/tty" || { echo "no bar: $(cat "$T/tty")" >&2; return 1; }
  [ "$(tail -c 1 "$T/tty" | od -An -c | tr -d ' ')" = '\n' ] || { echo "the bar's line was not finished" >&2; return 1; }
  ! grep -q 'PHASE\|STATUS' "$T/tty" || { echo "PHASE or STATUS printed: $(cat "$T/tty")" >&2; return 1; }
  grep -q 'installer: The install was successful\.' "$T/tty" || { echo "lost a line it does not recognise" >&2; return 1; }
}

descendants() {
  for p in $(pgrep -P "$1"); do
    echo "$p"
    descendants "$p"
  done
}

alive() {
  for p in $1; do
    kill -0 "$p" 2>/dev/null && return 0
  done
  return 1
}

# spec: src/install.sh lb_install -- its traps around the work dir are the caller's traps a step must keep.
interrupt_step() {
  # platform: a background job starts with INT ignored, and a signal ignored at entry cannot be trapped,
  #           so neither the caller's trap nor the step's would ever run; perl puts INT back first.
  # platform: the interrupted shell is not the pty's session leader: a leader's exit hangs up the
  #           terminal, and that SIGHUP would kill a leaked clock and hide it. The leader outlives it.
  rm -f "$T/child" "$T/workdir" "$T/leader"
  cat > "$T/interrupted.sh" <<'SH'
echo $$ > "$2/child"
. "$1/src/progress.sh"
d="$(mktemp -d "${TMPDIR:-/tmp}/longboat-work.XXXXXX")"
echo "$d" > "$2/workdir"
trap 'rm -rf "$d"' EXIT
trap 'exit 1' HUP INT TERM
lb_progress_init 1
SH
  printf '%s\n' "$1" >> "$T/interrupted.sh"
  perl -e '$SIG{INT} = "DEFAULT"; exec @ARGV or die "exec: $!"' script -q /dev/null \
    sh -c 'echo $$ > "$2/leader"; sh "$2/interrupted.sh" "$1" "$2"; sleep 10' sh "$root" "$T" \
    </dev/null >"$T/tty" 2>&1 &
  spid=$!
  i=0
  while [ ! -s "$T/workdir" ] && [ "$i" -lt 50 ]; do sleep 1; i=$((i + 1)); done
  sleep 2
  child="$(cat "$T/child")"
  kids="$(descendants "$child")"
  [ -n "$kids" ] || { echo "the step started nothing" >&2; return 1; }
  kill -s INT "$child"
  i=0
  while alive "$kids $child" && [ "$i" -lt 3 ]; do sleep 1; i=$((i + 1)); done
  rc=0
  if alive "$kids"; then
    echo "still alive after INT: $(ps -o pid,ppid,command -p "$(echo $kids | tr ' ' ',')" 2>/dev/null)" >&2
    rc=1
  fi
  [ "$(tail -c 1 "$T/tty" | od -An -c | tr -d ' ')" = '\n' ] || { echo "the step's line was left unfinished: $(cat "$T/tty")" >&2; rc=1; }
  leader="$(cat "$T/leader")"
  kill -s KILL $kids "$child" $(descendants "$leader") "$leader" "$spid" 2>/dev/null || :
  wait "$spid" 2>/dev/null || :
  d="$(cat "$T/workdir")"
  [ ! -e "$d" ] || { echo "the caller's EXIT trap did not remove its work dir $d" >&2; rm -rf "$d"; rc=1; }
  return "$rc"
}

test_interrupt_leaves_no_clock() {
  interrupt_step 'lb_step_run 1 x sleep 30'
}

test_interrupt_stops_a_functions_children() {
  interrupt_step 'f() { sleep 31; sleep 1; }; lb_step_run 1 x f'
}

test_interrupt_stops_a_scripts_children() {
  interrupt_step "lb_step_run 1 x sh -c 'sleep 32; :'"
}

for t in test_clock_ticks test_clock_line_fits_79_columns test_installer_bar_redraws test_interrupt_leaves_no_clock \
    test_interrupt_stops_a_functions_children test_interrupt_stops_a_scripts_children; do
  check "$t"
done
exit "$fail"
