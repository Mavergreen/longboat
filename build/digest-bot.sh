#!/bin/sh
# platform: macOS-only -- fill extracts the pinned shipyard pkg with pkgutil, through build/shipyard-scripts.sh
#   usage: digest-bot.sh fill           exit 0 printing nothing when components/shipyard/scripts.sha256 is
#                                       current for the pinned shipyard (shipyard-scripts.sh check exits 0);
#                                       when it is stale (check exits 3), print the new digest file's contents;
#                                       on any other check status, or a failure computing the digest, exit 1
#                                       having printed nothing. Progress goes to stderr.
#          digest-bot.sh apply <FILE>   write FILE over components/shipyard/scripts.sha256 and print changed,
#                                       or unchanged when it already holds FILE; refuse, writing nothing, a FILE
#                                       that is not one "<sha256>  <script>" line per listed script, in order.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
SS="$root/build/shipyard-scripts.sh"
LIST="$root/build/shipyard-scripts.list"
TARGET="$root/components/shipyard/scripts.sha256"

usage() {
  echo "usage: digest-bot.sh fill | apply <FILE>" >&2
  exit 2
}

well_formed() {
  file=$1
  [ -f "$file" ] || { echo "digest-bot: no $file" >&2; return 1; }
  awk 'length($1) != 64 || $1 ~ /[^0-9a-f]/ || NF != 2 || substr($0, 65, 2) != "  " { bad = 1 }
       END { exit bad }' "$file" \
    || { echo "digest-bot: $file has a line that is not \"<sha256>  <script>\"" >&2; return 1; }
  awk '{ print $2 }' "$file" > "$W/names"
  grep . "$LIST" > "$W/listed"
  cmp -s "$W/names" "$W/listed" \
    || { echo "digest-bot: $file does not name exactly the scripts in $LIST, in order" >&2; return 1; }
}

fill() {
  rc=0
  sh "$SS" check >&2 || rc=$?
  case "$rc" in
    0) echo "digest-bot: $TARGET is current: nothing to do" >&2; exit 0 ;;
    3) echo "digest-bot: $TARGET is stale: computing the new digest" >&2 ;;
    *) echo "digest-bot: shipyard-scripts.sh check exited $rc, an error rather than a stale digest: printing nothing" >&2; exit 1 ;;
  esac
  ver=$(cat "$root/components/shipyard/version")
  sh "$SS" extract "$ver" "$W/set" || { echo "digest-bot: cannot extract shipyard $ver" >&2; exit 1; }
  sh "$SS" digest "$W/set" > "$W/digest" || { echo "digest-bot: cannot digest shipyard $ver's scripts" >&2; exit 1; }
  well_formed "$W/digest" || exit 1
  cat "$W/digest"
}

apply() {
  well_formed "$1" || exit 1
  if cmp -s "$1" "$TARGET"; then
    echo unchanged
  else
    cat "$1" > "$TARGET"
    echo changed
  fi
}

[ "$#" -ge 1 ] || usage
cmd=$1; shift
case "$cmd" in
  fill) [ "$#" -eq 0 ] || usage ;;
  apply) [ "$#" -eq 1 ] || usage ;;
  *) usage ;;
esac
W=$(mktemp -d "${TMPDIR:-/tmp}/longboat-digest-bot.XXXXXX")
trap 'rm -rf "$W"' EXIT
trap 'exit 1' HUP INT TERM
case "$cmd" in
  fill) fill ;;
  apply) apply "$1" ;;
esac
