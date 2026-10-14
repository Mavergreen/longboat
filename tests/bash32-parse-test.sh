#!/bin/sh
# platform: macOS-only -- its /bin/bash is bash 3.2.57, the bash 10.9 ships and the /bin/sh every macOS
#           runs these scripts with; it refuses quoting that dash and every newer bash accept. Set
#           LONGBOAT_BASH32 to another bash 3.2 to run this anywhere
# spec: claude-plugins/mavergreen/skills/mavergreen-conventions/SKILL.md check 10 -- no shell construct 10.9 lacks.
#       This one is a parse error that only bash 3.2 reports: an apostrophe in a ${VAR:?word} inside
#       double quotes (build/install-go.sh, release run 38569568867), so only bash 3.2 can find it
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
bash32="${LONGBOAT_BASH32:-/bin/bash}"

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-bash32.XXXXXX")
trap 'rm -rf "$T"' EXIT

ver="$("$bash32" -c 'printf %s "$BASH_VERSION"' 2>/dev/null)" || ver=""
case "$ver" in
  3.2.*) ;;
  *) echo "SKIP: $bash32 is bash '${ver:-unknown}', not 3.2; set LONGBOAT_BASH32 to a bash 3.2"; exit 77 ;;
esac

# platform: the canary is the construct that broke release run 38569568867; a bash that accepts it would
#           make every file below look clean, so it must be refused first
printf ': "${X:?it%ss}"\n' "'" > "$T/canary.sh"
if "$bash32" --posix -n "$T/canary.sh" 2>/dev/null; then
  echo "FAIL: $bash32 ($ver) accepts the canary, so it cannot vouch for anything" >&2
  exit 1
fi

fail=0
n=0
git -C "$root" ls-files > "$T/files"
while IFS= read -r f; do
  case "$f" in
    *.sh) ;;
    *)
      [ -f "$root/$f" ] || continue
      head -n 1 "$root/$f" | grep -Eq '^#!(/bin/sh|/bin/bash|/usr/bin/env (ba)?sh)([[:space:]]|$)' || continue ;;
  esac
  n=$((n + 1))
  if ! "$bash32" --posix -n "$root/$f" 2>"$T/err"; then
    echo "FAIL: $f does not parse under bash $ver: $(cat "$T/err")" >&2
    fail=1
  fi
done < "$T/files"
[ "$n" -gt 0 ] || { echo "FAIL: no tracked shell script found, so this test checked nothing" >&2; exit 1; }
[ "$fail" -eq 0 ] && echo "ok: $n tracked shell scripts parse under bash $ver"
exit "$fail"
