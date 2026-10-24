#!/bin/sh
# platform: host-agnostic
# spec: docs/longboat.1 -- the manual page is the full reference: it names every command longboat --help
#       names, has the reference's sections, and is clean mdoc wherever mandoc can say so
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
PAGE="$root/docs/longboat.1"
T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-manpage.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

# platform: 10.9 has groff's nroff and no mandoc; modern macOS and most Linux have mandoc or groff. col -b drops
#           the overstrikes both use for bold and underline, and tr folds the page's line breaks away. groff 1.23
#           writes SGR escapes instead of overstrikes unless GROFF_NO_SGR is set; 10.9's groff ignores it
render() {
  if command -v mandoc >/dev/null 2>&1; then
    mandoc -Tascii "$PAGE"
  else
    GROFF_NO_SGR=1 nroff -Tascii -mdoc -rLL=250n "$PAGE"
  fi | col -b | tr -s ' \t\n' '   '
}

test_page_exists() {
  [ -f "$PAGE" ] || { echo "no $PAGE" >&2; return 1; }
}

test_names_every_help_command() {
  render > "$T/page" || return 1
  sh "$root/src/longboat" --help | sed -n 's/^[[:space:]]*\(usage: \)\{0,1\}\(longboat [a-z-].*\)$/\2/p' \
    | tr -d '<>' | tr -s ' ' > "$T/commands"
  [ -s "$T/commands" ] || { echo "longboat --help names no commands" >&2; return 1; }
  while IFS= read -r c; do
    grep -qF -- "$c" "$T/page" || { echo "the manual page does not name: $c" >&2; return 1; }
  done < "$T/commands"
}

test_has_the_reference_sections() {
  render > "$T/page" || return 1
  for s in NAME SYNOPSIS DESCRIPTION COMMANDS "MEDIA SOURCES" "EXIT STATUS" FILES ENVIRONMENT COMPATIBILITY "SEE ALSO"; do
    grep -qF " $s " "$T/page" || { echo "the manual page has no $s section" >&2; return 1; }
  done
}

test_names_the_paths_and_old_forms() {
  render > "$T/page" || return 1
  for w in /usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk /usr/local/mavergreen/var/longboat \
      "longboat install" "longboat fetch installer" "longboat verify installer" "--auth-header-env"; do
    grep -qF -- "$w" "$T/page" || { echo "the manual page does not name $w" >&2; return 1; }
  done
}

test_lints_clean() {
  command -v mandoc >/dev/null 2>&1 || { echo "skip: no mandoc" >&2; return 0; }
  out="$(mandoc -Tlint -Wwarning "$PAGE" 2>&1)" || true
  [ -z "$out" ] || { printf '%s\n' "$out" >&2; return 1; }
}

# spec: docs/longboat.1 SEE ALSO -- every .Xr names a page a reader's system has; mavergreen and packer have
#       none, so the page names them as commands instead
test_cross_references_exist() {
  sed -n 's/^\.Xr \([^ ]*\) .*/\1/p' "$PAGE" | sort -u > "$T/xr"
  printf '%s\n' clang curl shasum xz > "$T/known"
  comm -23 "$T/xr" "$T/known" > "$T/unknown"
  [ ! -s "$T/unknown" ] || { echo "the manual page cross-references pages that may not exist: $(cat "$T/unknown")" >&2; return 1; }
}

# spec: docs/longboat.1 COMPATIBILITY -- the old spellings are named there and nowhere else a reader looks
test_docs_use_the_new_forms() {
  for d in README.md INGREDIENTS.md docs/DEVELOPING.md; do
    if grep -nE 'longboat (install|fetch installer|verify installer|fetch [0-9])' "$root/$d" >&2; then
      echo "^ $d names an old form" >&2
      return 1
    fi
  done
}

for t in test_page_exists test_names_every_help_command test_has_the_reference_sections \
    test_names_the_paths_and_old_forms test_lints_clean test_cross_references_exist test_docs_use_the_new_forms; do
  check "$t"
done
exit "$fail"
