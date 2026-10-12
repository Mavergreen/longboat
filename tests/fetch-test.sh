#!/bin/sh
# platform: host-agnostic
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
LB="$root/src/longboat"
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"
LONGBOAT_SHIPYARD_DIR="${LONGBOAT_SHIPYARD_DIR:-$SHIPYARD_SCRIPTS}"
export LONGBOAT_SHIPYARD_DIR

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-fetch.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

mkdir -p "$T/src/MacOSX10.9.sdk/usr/include" "$T/src/MacOSX10.9.sdk/usr/lib" "$T/served"
echo "int x;" > "$T/src/MacOSX10.9.sdk/usr/include/stdio.h"
echo "stub" > "$T/src/MacOSX10.9.sdk/usr/lib/libSystem.dylib"
COPYFILE_DISABLE=1 tar cJf "$T/served/MacOSX10.9.sdk.tar.xz" -C "$T/src" MacOSX10.9.sdk
SHA=$(shasum -a 256 < "$T/served/MacOSX10.9.sdk.tar.xz" | cut -c1-64)
echo "sdk109 dev.mavergreen.longboat.sdk109 longboat" > "$T/with-sdk109"
echo "longboat dev.mavergreen.longboat longboat" > "$T/without-sdk109"
MAVERGREEN_PRODUCT_NAMES="$T/with-sdk109"
MAVERICKS_SDK_URL="file://$T/served/MacOSX10.9.sdk.tar.xz"
MAVERICKS_SDK_SHA256="$SHA"
export MAVERGREEN_PRODUCT_NAMES MAVERICKS_SDK_URL MAVERICKS_SDK_SHA256

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

test_list_offers_10_9() {
  [ "$(sh "$LB" list)" = "10.9 sdk109 available" ]
}

test_list_hides_unregistered() {
  [ -z "$(MAVERGREEN_PRODUCT_NAMES="$T/without-sdk109" sh "$LB" list)" ] || return 1
  cat "$T/without-sdk109" "$T/with-sdk109" > "$T/without-plus-sdk109"
  [ "$(MAVERGREEN_PRODUCT_NAMES="$T/without-plus-sdk109" sh "$LB" list)" = "10.9 sdk109 available" ] \
    || { echo "the same registry plus an sdk109 line must offer 10.9, or hiding proves nothing" >&2; return 1; }
}

make_broken_shipyards() {
  mkdir -p "$T/empty-shipyard" "$T/no-product-name"
  cp "$LONGBOAT_SHIPYARD_DIR/sdk-pins.sh" "$T/no-product-name/"
}

test_list_fails_on_broken_shipyard() {
  make_broken_shipyards
  for d in "$T/empty-shipyard" "$T/no-product-name"; do
    rc=0
    LONGBOAT_SHIPYARD_DIR="$d" sh "$LB" list >"$T/bout" 2>"$T/berr" || rc=$?
    [ "$rc" -ne 0 ] && [ ! -s "$T/bout" ] && [ -s "$T/berr" ] \
      || { echo "list with a broken shipyard dir $d exited $rc and said nothing on stderr" >&2; return 1; }
  done
  rc=0
  MAVERGREEN_PRODUCT_NAMES="$T/no-such-registry" sh "$LB" list >"$T/bout" 2>"$T/berr" || rc=$?
  [ "$rc" -ne 0 ] && [ ! -s "$T/bout" ] && [ -s "$T/berr" ] \
    || { echo "list with an unreadable registry exited $rc" >&2; return 1; }
}

test_fetch_on_broken_shipyard_is_not_unknown() {
  make_broken_shipyards
  rc=0
  LONGBOAT_SHIPYARD_DIR="$T/empty-shipyard" sh "$LB" fetch 10.9 --into "$T/brokenfetch" >/dev/null 2>"$T/berr" || rc=$?
  [ "$rc" -eq 1 ] && [ -s "$T/berr" ] && [ -z "$(grep "unknown SDK" "$T/berr")" ] \
    || { echo "a broken shipyard dir must fail as an error (1), not as an unknown SDK; got $rc: $(cat "$T/berr")" >&2; return 1; }
}

test_help_on_stdout() {
  sh "$LB" --help >"$T/help" 2>"$T/helperr" || return 1
  [ ! -s "$T/helperr" ] && grep -q 'install <version> \[--pkg-only DIR\]' "$T/help"
}

test_usage_error_on_stderr() {
  rc=0
  sh "$LB" bogus >"$T/uout" 2>"$T/uerr" || rc=$?
  [ "$rc" -eq 2 ] && [ ! -s "$T/uout" ] && grep -q 'install <version> \[--pkg-only DIR\]' "$T/uerr"
}

test_fetch_extracts() {
  out=$(sh "$LB" fetch 10.9 --into "$T/out")
  [ "$out" = "$T/out/MacOSX10.9.sdk" ] && [ -f "$T/out/MacOSX10.9.sdk/usr/include/stdio.h" ]
}

test_fetch_mismatch_leaves_nothing() {
  if MAVERICKS_SDK_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
      sh "$LB" fetch 10.9 --into "$T/bad" >/dev/null 2>&1; then
    return 1
  fi
  [ ! -e "$T/bad/MacOSX10.9.sdk" ] && sh "$LB" fetch 10.9 --into "$T/bad" >/dev/null
}

test_fetch_reuses_cache_offline() {
  cp "$T/served/MacOSX10.9.sdk.tar.xz" "$T/keep.tar.xz"
  sh "$LB" fetch 10.9 --into "$T/cached" >/dev/null
  rm "$T/served/MacOSX10.9.sdk.tar.xz"
  rc=0
  sh "$LB" fetch 10.9 --into "$T/cached" >/dev/null || rc=1
  cp "$T/keep.tar.xz" "$T/served/MacOSX10.9.sdk.tar.xz"
  [ "$rc" -eq 0 ]
}

test_unknown_sdk() {
  for v in 10.6 10.9.5 MacOSX10.9.sdk ""; do
    rc=0
    sh "$LB" fetch "$v" --into "$T/unk" 2>"$T/err" >/dev/null || rc=$?
    [ "$rc" -eq 2 ] && grep -q '10\.9' "$T/err" || return 1
  done
}

test_paths_with_spaces() {
  mkdir -p "$T/tmp space"
  out=$(TMPDIR="$T/tmp space" sh "$LB" fetch 10.9 --into "$T/with space")
  [ "$out" = "$T/with space/MacOSX10.9.sdk" ] && [ -f "$out/usr/include/stdio.h" ]
}

test_runs_through_symlink() {
  mkdir -p "$T/farm/bin" "$T/farm/longboat/bin"
  cp "$LB" "$T/farm/longboat/bin/longboat"
  mkdir -p "$T/farm/longboat/libexec/longboat"
  cp "$root"/src/sdks.sh "$root"/src/fetch.sh "$root"/src/install.sh "$T/farm/longboat/libexec/longboat/"
  ln -s ../longboat/bin/longboat "$T/farm/bin/longboat"
  sh "$T/farm/bin/longboat" --help >/dev/null 2>&1 && sh "$T/farm/bin/longboat" list | grep -qx '10.9 sdk109 available'
}

for t in test_list_offers_10_9 test_list_hides_unregistered test_list_fails_on_broken_shipyard \
    test_fetch_on_broken_shipyard_is_not_unknown test_help_on_stdout test_usage_error_on_stderr \
    test_fetch_extracts test_fetch_mismatch_leaves_nothing test_fetch_reuses_cache_offline \
    test_unknown_sdk test_paths_with_spaces test_runs_through_symlink; do
  check "$t"
done
exit "$fail"
