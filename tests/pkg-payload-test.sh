#!/bin/sh
# platform: macOS-only -- expands the built pkg with pkgutil and unpacks its payloads with cpio
# spec: .superpowers/specs/2026-10-12-longboat-design.md "The Longboat product" and "Tests"
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
pkg=""
for p in dist/longboat-*.pkg; do [ -f "$p" ] && pkg=$p; done
[ -n "$pkg" ] || { echo "SKIP: no dist/longboat-*.pkg; run build/package.sh"; exit 77; }
ver=${pkg#dist/longboat-}
ver=${ver%.pkg}

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-payload.XXXXXX")
trap 'rm -rf "$T"' EXIT
pkgutil --expand "$pkg" "$T/x" >/dev/null 2>&1 || { echo "FAIL: cannot expand $pkg" >&2; exit 1; }
mkdir "$T/p"
find "$T/x" -type f -name Payload > "$T/payloads"
while IFS= read -r payload; do
  gzip -dc "$payload" > "$T/raw" 2>/dev/null || { echo "FAIL: cannot decompress $payload" >&2; exit 1; }
  (cd "$T/p" && cpio -id --quiet < "$T/raw") >/dev/null 2>&1 || { echo "FAIL: cannot unpack $payload" >&2; exit 1; }
done < "$T/payloads"
L="$T/p/usr/local/mavergreen/longboat"

fail=0
check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

test_payload_has_cli() {
  [ -f "$L/bin/longboat" ] || return 1
  for f in sdks.sh fetch.sh install.sh VERSION; do
    [ -f "$L/libexec/longboat/$f" ] || return 1
  done
  [ "$(cat "$L/libexec/longboat/VERSION")" = "$ver" ]
}

test_shipped_copies_match_digest() {
  sh build/shipyard-scripts.sh digest "$L/libexec/longboat/shipyard" > "$T/digest" || return 1
  diff components/shipyard/scripts.sha256 "$T/digest" >&2
}

test_no_sdk_bytes() {
  (cd "$T/p" && find . -print) | grep -E '\.sdk/|\.sdk\.tar\.' > "$T/sdkhits" || true
  [ ! -s "$T/sdkhits" ]
}

test_shipyard_version_staged() {
  [ "$(cat "$L/libexec/longboat/SHIPYARD_VERSION")" = "$(sed 's/^v//' components/shipyard/version)" ]
}

test_installed_cli_runs() {
  mkdir "$T/lbroot"
  env -u LONGBOAT_SHIPYARD_DIR LONGBOAT_ROOT="$T/lbroot" sh "$L/bin/longboat" list > "$T/list" 2>"$T/listerr" || { cat "$T/listerr" >&2; return 1; }
  grep -qx '10.9 sdk109 available' "$T/list"
}

test_installed_cli_help() {
  env -u LONGBOAT_SHIPYARD_DIR sh "$L/bin/longboat" --help >/dev/null 2>&1
}

test_farm_symlink_runs() {
  mkdir -p "$T/p/usr/local/mavergreen/bin" "$T/lbroot2"
  ln -s ../longboat/bin/longboat "$T/p/usr/local/mavergreen/bin/longboat"
  env -u LONGBOAT_SHIPYARD_DIR LONGBOAT_ROOT="$T/lbroot2" sh "$T/p/usr/local/mavergreen/bin/longboat" list > "$T/list2" 2>"$T/listerr2" || { cat "$T/listerr2" >&2; return 1; }
  grep -qx '10.9 sdk109 available' "$T/list2" \
    && env -u LONGBOAT_SHIPYARD_DIR sh "$T/p/usr/local/mavergreen/bin/longboat" --help >/dev/null 2>&1
}

check test_payload_has_cli
check test_shipped_copies_match_digest
check test_no_sdk_bytes
check test_shipyard_version_staged
check test_installed_cli_runs
check test_installed_cli_help
check test_farm_symlink_runs
exit "$fail"
