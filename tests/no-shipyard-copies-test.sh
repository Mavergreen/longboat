#!/bin/sh
# platform: host-agnostic
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
digest=components/shipyard/scripts.sha256
list=build/shipyard-scripts.list
[ -f "$digest" ] && [ -f "$list" ] || { echo "FAIL: $digest or $list missing" >&2; exit 1; }

T=$(mktemp -d "${TMPDIR:-/tmp}/nsc.XXXXXX")
trap 'rm -rf "$T"' EXIT
git ls-files > "$T/files" || { echo "FAIL: git ls-files failed" >&2; exit 1; }
[ -s "$T/files" ] || { echo "FAIL: git ls-files lists nothing" >&2; exit 1; }
sed 's|.*/||' "$list" | grep . > "$T/names"
fail=0
while IFS= read -r f; do
  [ "$f" = build/msc.sh ] && continue
  [ -f "$f" ] || continue
  sum=$(shasum -a 256 < "$f" | cut -c1-64)
  base=$(basename "$f")
  if grep -q "^$sum  " "$digest" || grep -Fxq -- "$base" "$T/names"; then
    echo "FAIL: $f: Longboat must not carry a copy of a shipyard script (build/shipyard-scripts.sh copies them from the pinned shipyard release at package time, so a tracked copy would drift from what ships)" >&2
    fail=1
  fi
done < "$T/files"
[ "$fail" -eq 0 ] && echo "ok: no shipyard copies"
exit "$fail"
