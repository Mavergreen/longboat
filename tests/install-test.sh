#!/bin/sh
# platform: macOS-only -- builds real pkgs with pkgbuild/productbuild and reads them with pkgutil
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
LB="$root/src/longboat"
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-install.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0
mkdir -p "$T/tmp space"
TMPDIR="$T/tmp space"
export TMPDIR

if [ -z "${LONGBOAT_SHIPYARD_DIR:-}" ]; then
  if (cd "$root" && sh build/shipyard-scripts.sh extract "$(cat components/shipyard/version)" "$T/shipyard") >/dev/null 2>"$T/extract-err"; then
    LONGBOAT_SHIPYARD_DIR="$T/shipyard"
  elif [ -n "${CI:-}" ]; then
    cat "$T/extract-err" >&2
    echo "FAIL: cannot extract the pinned shipyard; in CI install must be tested against the scripts Longboat ships, never the installed shipyard (does the Tests step have GH_TOKEN?)" >&2
    exit 1
  else
    echo "notice: cannot extract pinned shipyard offline; using installed scripts at $SHIPYARD_SCRIPTS" >&2
    LONGBOAT_SHIPYARD_DIR="$SHIPYARD_SCRIPTS"
  fi
fi
export LONGBOAT_SHIPYARD_DIR

mkdir -p "$T/src/MacOSX10.9.sdk/usr/include" "$T/src/MacOSX10.9.sdk/usr/lib" "$T/served"
echo "int x;" > "$T/src/MacOSX10.9.sdk/usr/include/stdio.h"
echo "stub" > "$T/src/MacOSX10.9.sdk/usr/lib/libSystem.dylib"
COPYFILE_DISABLE=1 tar cJf "$T/served/MacOSX10.9.sdk.tar.xz" -C "$T/src" MacOSX10.9.sdk
SHA=$(shasum -a 256 < "$T/served/MacOSX10.9.sdk.tar.xz" | cut -c1-64)
echo "sdk109 dev.mavergreen.longboat.sdk109 longboat" > "$T/with-sdk109"
MAVERGREEN_PRODUCT_NAMES="$T/with-sdk109"
MAVERICKS_SDK_URL="file://$T/served/MacOSX10.9.sdk.tar.xz"
MAVERICKS_SDK_SHA256="$SHA"
LONGBOAT_VERSION=20261012.1
LONGBOAT_ROOT="$T/root"
export MAVERGREEN_PRODUCT_NAMES MAVERICKS_SDK_URL MAVERICKS_SDK_SHA256 LONGBOAT_VERSION LONGBOAT_ROOT

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

built() { [ -f "$T/dist/sdk109-20261012.1.pkg" ] || sh "$LB" install 10.9 --pkg-only "$T/dist" >/dev/null; }

test_install_needs_root() {
  [ "$(id -u)" -ne 0 ] || { echo "skip: running as root" >&2; return 0; }
  rc=0
  sh "$LB" install 10.9 2>"$T/err" >/dev/null || rc=$?
  [ "$rc" -eq 2 ] && grep -q 'sudo longboat install 10.9' "$T/err" && [ ! -e "$T/root" ]
}

test_pkg_only_builds_conformant_pkg() {
  built || return 1
  [ -f "$T/dist/sdk109-20261012.1.pkg" ] || return 1
  mkdir -p "$T/repo"
  printf '## Conformance deviations\n\n- scheme: Longboat is its own upstream (no upstream release to repackage), so it versions itself by date as YYYYMMDD.N with no -mavericks.N axis\n' > "$T/repo/INGREDIENTS.md"
  facts="$(sh "$SHIPYARD_SCRIPTS/artifact-facts.sh" "$T/dist" 20261012.1 "$T/repo")" || return 1
  printf '%s\n' "$facts" | sh "$SHIPYARD_SCRIPTS/check-artifact-conformance.sh"
}

test_pkg_payload() {
  built || { echo "test_pkg_payload: the package was not built" >&2; return 1; }
  pkg="$T/dist/sdk109-20261012.1.pkg"
  rm -rf "$T/expanded" "$T/payload"
  pkgutil --expand "$pkg" "$T/expanded" || { echo "test_pkg_payload: pkgutil cannot expand $pkg" >&2; return 1; }
  comp="$(grep -l 'dev\.mavergreen\.longboat\.sdk109' "$T"/expanded/*.pkg/PackageInfo | head -1)"
  [ -n "$comp" ] || { echo "test_pkg_payload: no component PackageInfo names dev.mavergreen.longboat.sdk109" >&2; return 1; }
  mkdir "$T/payload"
  gzip -dc "$(dirname "$comp")/Payload" > "$T/payload.cpio" \
    || { echo "test_pkg_payload: cannot decompress the sdk109 component's payload" >&2; return 1; }
  # platform: modern pkgutil --payload-files lists only a product archive's first component (the base), so
  #           the sdk109 component's payload is listed from its own cpio.
  files="$(cpio -it < "$T/payload.cpio" 2>/dev/null)" \
    || { echo "test_pkg_payload: cannot list the sdk109 component's payload" >&2; return 1; }
  printf '%s\n' "$files" | grep -qx './usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk/usr/include/stdio.h' \
    || { echo "test_pkg_payload: payload lacks the SDK's stdio.h" >&2; return 1; }
  printf '%s\n' "$files" | grep -qx './usr/local/mavergreen/sdk109/mavergreen.plist' \
    || { echo "test_pkg_payload: payload lacks sdk109/mavergreen.plist" >&2; return 1; }
  stray="$(printf '%s\n' "$files" | grep -v -e '^\.$' -e '^\./usr$' -e '^\./usr/local$' \
    -e '^\./usr/local/mavergreen$' -e '^\./usr/local/mavergreen/sdk109\(/\|$\)' || true)"
  [ -z "$stray" ] || { echo "test_pkg_payload: stray paths outside sdk109 in its component:" >&2; echo "$stray" >&2; return 1; }
  ( cd "$T/payload" && cpio -id ./usr/local/mavergreen/sdk109/mavergreen.plist < "$T/payload.cpio" 2>/dev/null ) \
    || { echo "test_pkg_payload: cannot extract the manifest" >&2; return 1; }
  plist="$T/payload/usr/local/mavergreen/sdk109/mavergreen.plist"
  [ -f "$plist" ] || { echo "test_pkg_payload: manifest was not extracted" >&2; return 1; }
  /usr/libexec/PlistBuddy -c 'Print :appcast' "$plist" >/dev/null 2>&1 \
    || { echo "test_pkg_payload: manifest has no appcast key" >&2; return 1; }
  [ -z "$(/usr/libexec/PlistBuddy -c 'Print :appcast' "$plist")" ] \
    || { echo "test_pkg_payload: manifest appcast is not empty" >&2; return 1; }
}

test_home_untouched() {
  mkdir -p "$T/home"
  rm -rf "$T/dist2"
  HOME="$T/home" sh "$LB" install 10.9 --pkg-only "$T/dist2" >/dev/null || return 1
  [ -z "$(ls -A "$T/home")" ]
}

test_install_failure_leaves_nothing() {
  mkdir -p "$T/fdist"
  echo old > "$T/fdist/sdk109-old.pkg"
  rc=0
  MAVERICKS_SDK_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
    sh "$LB" install 10.9 --pkg-only "$T/fdist" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || return 1
  [ "$(ls "$T"/fdist/*.pkg)" = "$T/fdist/sdk109-old.pkg" ] && [ "$(cat "$T/fdist/sdk109-old.pkg")" = old ] || return 1
  [ -z "$(ls -A "$T/fdist/cache" 2>/dev/null)" ] || return 1
  [ -z "$(ls -A "$T/tmp space")" ]
}

test_root_install_failure_leaves_installed_sdk() {
  [ "$(id -u)" -eq 0 ] || { echo "skip: needs root to take the real install path; hardware checks cover it" >&2; return 0; }
  sdk="$T/root/usr/local/mavergreen/sdk109"
  mkdir -p "$sdk/SDKs/MacOSX10.9.sdk"
  echo old > "$sdk/mavergreen.plist"
  rc=0
  MAVERICKS_SDK_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
    sh "$LB" install 10.9 >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || return 1
  [ "$(cat "$sdk/mavergreen.plist")" = old ] && [ -d "$sdk/SDKs/MacOSX10.9.sdk" ] || return 1
  [ -z "$(ls -A "$T/root/usr/local/mavergreen/var/longboat" 2>/dev/null)" ] || return 1
  [ -z "$(ls -A "$T/tmp space")" ]
}

test_missing_version_exits_2() {
  rc=0
  env -u LONGBOAT_VERSION sh "$LB" install 10.9 --pkg-only "$T/nover" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 2 ]
}

for t in test_install_needs_root test_pkg_only_builds_conformant_pkg test_pkg_payload \
    test_home_untouched test_install_failure_leaves_nothing test_root_install_failure_leaves_installed_sdk test_missing_version_exits_2; do
  check "$t"
done
exit "$fail"
