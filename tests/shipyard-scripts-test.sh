#!/bin/sh
# platform: macOS-only -- builds a fixture pkg with pkgbuild and expands it with pkgutil
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
SS="$root/build/shipyard-scripts.sh"
LIST="$root/build/shipyard-scripts.list"
T=$(mktemp -d "${TMPDIR:-/tmp}/longboat ss.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

SCRIPTS=usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts
while IFS= read -r p; do
  [ -n "$p" ] || continue
  mkdir -p "$T/root/$SCRIPTS/$(dirname "$p")"
  printf 'fixture %s\n' "$p" > "$T/root/$SCRIPTS/$p"
done < "$LIST"
pkgbuild --quiet --root "$T/root" --identifier test.fixture --version 1 "$T/fixture.pkg" >/dev/null 2>&1
PKGSUM=$(shasum -a 256 < "$T/fixture.pkg" | cut -c1-64)
printf '%s  fixture.pkg\n' "$PKGSUM" > "$T/SHA256SUMS"
LONGBOAT_SHIPYARD_PKG="$T/fixture.pkg"
LONGBOAT_SHIPYARD_SUMS="$T/SHA256SUMS"
export LONGBOAT_SHIPYARD_PKG LONGBOAT_SHIPYARD_SUMS

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

test_extract_copies_listed_files() {
  rm -rf "$T/out"
  sh "$SS" extract v0.0.1 "$T/out" || return 1
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    cmp -s "$T/out/$p" "$T/root/$SCRIPTS/$p" || { echo "differs or missing: $p" >&2; return 1; }
  done < "$LIST"
}

test_extract_refuses_unlisted_pkg() {
  printf '%s  other.pkg\n' "$PKGSUM" > "$T/sums-unlisted"
  rm -rf "$T/out2"
  if LONGBOAT_SHIPYARD_SUMS="$T/sums-unlisted" sh "$SS" extract v0.0.1 "$T/out2" 2>/dev/null; then return 1; fi
  [ ! -e "$T/out2" ]
}

test_extract_refuses_sha_mismatch() {
  printf '%s  fixture.pkg\n' "0000000000000000000000000000000000000000000000000000000000000000" > "$T/sums-bad"
  rm -rf "$T/out3"
  if LONGBOAT_SHIPYARD_SUMS="$T/sums-bad" sh "$SS" extract v0.0.1 "$T/out3" 2>/dev/null; then return 1; fi
  [ ! -e "$T/out3" ]
}

test_extract_refuses_missing_listed_file() {
  mkdir -p "$T/root2"
  cp -R "$T/root/." "$T/root2/"
  rm "$T/root2/$SCRIPTS/product-names"
  pkgbuild --quiet --root "$T/root2" --identifier test.fixture --version 1 "$T/missing.pkg" >/dev/null 2>&1
  printf '%s  missing.pkg\n' "$(shasum -a 256 < "$T/missing.pkg" | cut -c1-64)" > "$T/sums-missing"
  rm -rf "$T/out4"
  err=$(LONGBOAT_SHIPYARD_PKG="$T/missing.pkg" LONGBOAT_SHIPYARD_SUMS="$T/sums-missing" sh "$SS" extract v0.0.1 "$T/out4" 2>&1) && return 1
  case "$err" in *product-names*) ;; *) echo "error does not name the file: $err" >&2; return 1 ;; esac
}

test_digest_is_list_ordered() {
  rm -rf "$T/out5"
  sh "$SS" extract v0.0.1 "$T/out5" || return 1
  sh "$SS" digest "$T/out5" > "$T/digest" || return 1
  n=$(grep -c . "$LIST")
  [ "$(wc -l < "$T/digest" | tr -d ' ')" -eq "$n" ] || return 1
  grep . "$LIST" | awk '{print $0}' > "$T/names"
  awk '{ $1=""; sub(/^ +/, ""); print }' "$T/digest" > "$T/dnames"
  cmp -s "$T/names" "$T/dnames"
}

test_check_reports_stale() {
  printf '%s\n' "0000  product-names" > "$T/committed-stale"
  rc=0
  LONGBOAT_SCRIPTS_SHA256="$T/committed-stale" sh "$SS" check >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 3 ] || return 1
  rm -rf "$T/out6"
  sh "$SS" extract v0.0.1 "$T/out6" && sh "$SS" digest "$T/out6" > "$T/committed-ok" || return 1
  LONGBOAT_SCRIPTS_SHA256="$T/committed-ok" sh "$SS" check >/dev/null 2>&1
}

test_check_errors_without_committed_digest() {
  rc=0
  LONGBOAT_SCRIPTS_SHA256="$T/does not exist" sh "$SS" check >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 1 ]
}

test_extract_refuses_ambiguous_components() {
  mkdir -p "$T/comp1" "$T/comp2" "$T/flat"
  cp -R "$T/root/." "$T/comp1/"
  cp -R "$T/root/." "$T/comp2/"
  pkgbuild --quiet --root "$T/comp1" --identifier test.a --version 1 "$T/flat/a.pkg" >/dev/null 2>&1
  pkgbuild --quiet --root "$T/comp2" --identifier test.b --version 1 "$T/flat/b.pkg" >/dev/null 2>&1
  printf "<?xml version=\"1.0\" encoding=\"utf-8\"?><installer-gui-script minSpecVersion=\"1\"><pkg-ref id=\"test.a\"/><pkg-ref id=\"test.b\"/><choices-outline><line choice=\"default\"/></choices-outline><choice id=\"default\"><pkg-ref id=\"test.a\"/><pkg-ref id=\"test.b\"/></choice><pkg-ref id=\"test.a\">a.pkg</pkg-ref><pkg-ref id=\"test.b\">b.pkg</pkg-ref></installer-gui-script>\n" > "$T/flat/dist.xml"
  productbuild --quiet --distribution "$T/flat/dist.xml" --package-path "$T/flat" "$T/two.pkg" >/dev/null 2>&1 || { echo "cannot build product pkg" >&2; return 1; }
  printf "%s  two.pkg\n" "$(shasum -a 256 < "$T/two.pkg" | cut -c1-64)" > "$T/sums-two"
  rm -rf "$T/out7"
  err=$(LONGBOAT_SHIPYARD_PKG="$T/two.pkg" LONGBOAT_SHIPYARD_SUMS="$T/sums-two" sh "$SS" extract v0.0.1 "$T/out7" 2>&1) && return 1
  case "$err" in *"2 components"*) ;; *) echo "wrong error: $err" >&2; return 1 ;; esac
  [ ! -e "$T/out7" ]
}

check test_check_errors_without_committed_digest
check test_extract_refuses_ambiguous_components
check test_extract_copies_listed_files
check test_extract_refuses_unlisted_pkg
check test_extract_refuses_sha_mismatch
check test_extract_refuses_missing_listed_file
check test_digest_is_list_ordered
check test_check_reports_stale
exit "$fail"
