#!/bin/sh
# platform: host-agnostic
#   usage: . progress.sh     (sourced; everything it prints goes to stderr)
#          lb_progress_init <total>                   once, before the first step
#          lb_step_start <n> <label>                  the start line; at a terminal left open for a redraw
#          lb_step_end <n> <label> <rc>               the done or failed line, timed from lb_step_start
#          lb_step_run <n> <label> <cmd> [args...]    a silent step: its output is held and shown only if
#                                                     it fails; a ticking clock at a terminal
#          lb_step_show <n> <label> <cmd> [args...]   a step with progress of its own: output passes through
#          lb_step_note <n> <label> <note>            one finished line for a step that did nothing
#          installer -verboseR ... | lb_installer_bar <n> <label>
#                                                     a one-line percentage bar at a terminal; in a log,
#                                                     every line unchanged
# spec: tests/progress-test.sh, tests/progress-tty-test.sh -- the line formats, held output, the clock,
#       the bar, and a clock that never outlives its step.

lb_progress_init() {
  LB_PROGRESS_TOTAL="$1"
  LB_PROGRESS_TTY=""
  [ ! -t 2 ] || LB_PROGRESS_TTY=1
}

# spec: tests/progress-tty-test.sh test_clock_line_fits_79_columns -- at a terminal the label is cut so
#       the longest line it heads, "[n/T] <label>: failed (MM:SS)", fits 79 columns and never wraps,
#       since a wrapped line cannot be redrawn with a carriage return.
_lb_step_head() {
  _lb_head="[$1/${LB_PROGRESS_TOTAL:-?}] "
  if [ -n "${LB_PROGRESS_TTY:-}" ]; then
    printf '%s%.*s' "$_lb_head" "$((79 - ${#_lb_head} - 16))" "$2"
  else
    printf '%s%s' "$_lb_head" "$2"
  fi
}

_lb_since() {
  _lb_secs=$(($(date +%s) - $1))
  printf '%d:%02d' "$((_lb_secs / 60))" "$((_lb_secs % 60))"
}

lb_step_start() {
  _lb_step_t0="$(date +%s)"
  _lb_step_line="$(_lb_step_head "$1" "$2")"
  if [ -n "${LB_PROGRESS_TTY:-}" ]; then
    printf '%s...' "$_lb_step_line" >&2
  else
    printf '%s...\n' "$_lb_step_line" >&2
  fi
}

lb_step_end() {
  _lb_word=done
  [ "$3" -eq 0 ] || _lb_word=failed
  [ -z "${LB_PROGRESS_TTY:-}" ] || printf '\r' >&2
  printf '%s: %s (%s)\n' "$(_lb_step_head "$1" "$2")" "$_lb_word" \
    "$(_lb_since "${_lb_step_t0:-$(date +%s)}")" >&2
}

lb_step_note() {
  printf '%s: %s\n' "$(_lb_step_head "$1" "$2")" "$3" >&2
}

lb_step_show() {
  _lb_show_n="$1" _lb_show_label="$2"
  shift 2
  lb_step_start "$_lb_show_n" "$_lb_show_label"
  [ -z "${LB_PROGRESS_TTY:-}" ] || printf '\n' >&2
  _lb_show_rc=0
  "$@" || _lb_show_rc=$?
  lb_step_end "$_lb_show_n" "$_lb_show_label" "$_lb_show_rc"
  return "$_lb_show_rc"
}

_lb_step_clock() {
  while sleep 1; do
    printf '\r%s... (%s)' "$_lb_step_line" "$(_lb_since "$_lb_step_t0")" >&2
  done
}

# spec: tests/progress-tty-test.sh test_interrupt_stops_a_functions_children,
#       test_interrupt_stops_a_scripts_children -- a step's command starts with INT ignored (it is an
#       async list) and so does everything it starts, so a terminal's Ctrl-C reaches none of it.
#       Killing only the top would orphan the workers (ditto, pkgbuild) to write on into a work dir
#       install is removing. The tree is stopped top down first, so nothing in it can fork or be
#       reparented out of pgrep's sight, then terminated and continued.
_lb_tree_freeze() {
  kill -s STOP "$1" 2>/dev/null || return 0
  echo "$1"
  for _lb_kid in $(pgrep -P "$1" 2>/dev/null); do
    _lb_tree_freeze "$_lb_kid"
  done
}

_lb_tree_kill() {
  _lb_tree="$(_lb_tree_freeze "$1")"
  [ -n "$_lb_tree" ] || return 0
  kill -s TERM $_lb_tree 2>/dev/null || :
  kill -s CONT $_lb_tree 2>/dev/null || :
  wait "$1" 2>/dev/null || :
}

# spec: tests/progress-tty-test.sh test_interrupt_leaves_no_clock -- stops the clock and the command,
#       finishes the line, and gives the caller back its own traps. Every path out of a step comes
#       through here once; the second call finds nothing to do.
_lb_step_stop() {
  [ -n "${_lb_step_dir:-}" ] || return 0
  if [ -n "${_lb_step_clock:-}" ]; then
    _lb_tree_kill "$_lb_step_clock"
  fi
  if [ -n "${_lb_step_pid:-}" ]; then
    _lb_tree_kill "$_lb_step_pid"
  fi
  lb_step_end "$_lb_step_n" "$_lb_step_label" "$1" || :
  [ "$1" -eq 0 ] || [ -z "$2" ] || cat "$_lb_step_dir/out" >&2 || :
  trap - EXIT HUP INT TERM
  . "$_lb_step_dir/traps"
  rm -rf "$_lb_step_dir"
  _lb_step_dir="" _lb_step_clock="" _lb_step_pid=""
}

# spec: tests/progress-tty-test.sh test_interrupt_leaves_no_clock -- a step must not clobber the
#       caller's trap (install's work-dir cleanup): on the signal it stops the step, then does what the
#       caller's trap does, or, with none, dies of the signal as the caller would have.
_lb_step_trap() {
  _lb_trap_was="$(sed -n "s/^trap -- \\(.*\\) \\(SIG\\)\\{0,1\\}$1\$/\\1/p" "$_lb_step_dir/traps")"
  case "$1:$_lb_trap_was" in
    *:"''") return 0 ;;
    EXIT:) _lb_trap_then=":" ;;
    *:) _lb_trap_then="trap - $1; kill -s $1 \$\$" ;;
    *) _lb_trap_then="eval $_lb_trap_was" ;;
  esac
  trap "_lb_step_stop 130 ''; $_lb_trap_then" "$1"
}

lb_step_run() {
  _lb_step_n="$1" _lb_step_label="$2"
  shift 2
  _lb_step_dir="$(mktemp -d "${TMPDIR:-/tmp}/longboat-step.XXXXXX")" || return 1
  _lb_step_clock="" _lb_step_pid=""
  trap > "$_lb_step_dir/traps"
  for _lb_sig in EXIT HUP INT TERM; do
    _lb_step_trap "$_lb_sig"
  done
  lb_step_start "$_lb_step_n" "$_lb_step_label"
  if [ -n "${LB_PROGRESS_TTY:-}" ]; then
    _lb_step_clock &
    _lb_step_clock=$!
  fi
  # platform: the command runs in the background and the step waits for it, because POSIX defers a
  #           trap until a foreground command finishes; only an interrupted wait lets the trap
  #           stop the clock at once.
  "$@" >"$_lb_step_dir/out" 2>&1 &
  _lb_step_pid=$!
  _lb_step_rc=0
  wait "$_lb_step_pid" || _lb_step_rc=$?
  [ -n "$_lb_step_dir" ] || return "$_lb_step_rc"
  _lb_step_pid=""
  _lb_step_stop "$_lb_step_rc" held
  return "$_lb_step_rc"
}

lb_installer_bar() {
  _lb_bar_head="$(_lb_step_head "$1" "$2")"
  _lb_bar_open=1
  while IFS= read -r _lb_bar_l || [ -n "$_lb_bar_l" ]; do
    if [ -z "${LB_PROGRESS_TTY:-}" ]; then
      printf '%s\n' "$_lb_bar_l" >&2
      continue
    fi
    _lb_bar_p="${_lb_bar_l#installer:%}"
    _lb_bar_p="${_lb_bar_p%%.*}"
    case "$_lb_bar_l:$_lb_bar_p" in
      installer:%*:[0-9]*)
        printf '\r%s  %s%%' "$_lb_bar_head" "$_lb_bar_p" >&2
        _lb_bar_open=1 ;;
      installer:PHASE:*|installer:STATUS:*) ;;
      *)
        [ -z "$_lb_bar_open" ] || printf '\n' >&2
        printf '%s\n' "$_lb_bar_l" >&2
        _lb_bar_open="" ;;
    esac
  done
  [ -z "${LB_PROGRESS_TTY:-}" ] || [ -z "$_lb_bar_open" ] || printf '\n' >&2
}
