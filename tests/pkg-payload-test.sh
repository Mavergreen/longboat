#!/bin/sh
# platform: macOS-only -- expands the built pkg with pkgutil and unpacks its payloads with cpio
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"
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

component_version() {
  for info in "$1"/PackageInfo "$1"/*/PackageInfo; do
    [ -f "$info" ] || continue
    tr '\n' ' ' < "$info" | sed -n "s/.*<pkg-info[^>]* identifier=\"$2\"[^>]*>.*/&/p" \
      | sed -n 's/.*<pkg-info[^>]* version="\([^"]*\)".*/\1/p'
  done
}

test_base_is_the_packaging_shipyards() {
  mkdir "$T/base"
  sh "$SHIPYARD_SCRIPTS/build-base-component.sh" --out "$T/base/base.pkg" >/dev/null 2>&1 || return 1
  pkgutil --expand "$T/base/base.pkg" "$T/base/x" >/dev/null 2>&1 || return 1
  want="$(component_version "$T/base/x" dev.mavergreen.base)"
  got="$(component_version "$T/x" dev.mavergreen.base)"
  [ -n "$want" ] && [ "$want" = "$got" ] \
    || { echo "the base component carries the packaging shipyard's helper, so it must carry that shipyard's version ($want), not the pin's; got '$got'" >&2; return 1; }
}

test_shipped_set_installs() {
  mkdir -p "$T/sdk/src/MacOSX10.9.sdk/usr/include" "$T/sdk/src/MacOSX10.9.sdk/usr/lib" "$T/sdk/served" "$T/sdk/home"
  echo "int x;" > "$T/sdk/src/MacOSX10.9.sdk/usr/include/stdio.h"
  echo "stub" > "$T/sdk/src/MacOSX10.9.sdk/usr/lib/libSystem.dylib"
  COPYFILE_DISABLE=1 tar cJf "$T/sdk/served/MacOSX10.9.sdk.tar.xz" -C "$T/sdk/src" MacOSX10.9.sdk
  sha=$(shasum -a 256 < "$T/sdk/served/MacOSX10.9.sdk.tar.xz" | cut -c1-64)
  out="$T/sdk/dist with space"
  env -u LONGBOAT_SHIPYARD_DIR -u LONGBOAT_VERSION -u MAVERGREEN_PRODUCT_NAMES HOME="$T/sdk/home" \
    MAVERICKS_SDK_URL="file://$T/sdk/served/MacOSX10.9.sdk.tar.xz" MAVERICKS_SDK_SHA256="$sha" \
    sh "$L/bin/longboat" install 10.9 --pkg-only "$out" > "$T/sdk/out" 2> "$T/sdk/err" \
    || { cat "$T/sdk/err" >&2; echo "the shipped scripts alone could not build an SDK pkg" >&2; return 1; }
  [ "$(cat "$T/sdk/out")" = "$out/sdk109-$ver.pkg" ] && [ -f "$out/sdk109-$ver.pkg" ] || return 1
  facts="$(sh "$SHIPYARD_SCRIPTS/artifact-facts.sh" "$out" "$ver" "$root")" || return 1
  printf '%s\n' "$facts" | sh "$SHIPYARD_SCRIPTS/check-artifact-conformance.sh" >&2
}

check test_payload_has_cli
check test_base_is_the_packaging_shipyards
check test_shipped_set_installs
check test_shipped_copies_match_digest
check test_no_sdk_bytes
check test_shipyard_version_staged
check test_installed_cli_runs
check test_installed_cli_help
check test_farm_symlink_runs
exit "$fail"
