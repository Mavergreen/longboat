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
# spec: install-test.sh's LONGBOAT_ROOT -- list asks LONGBOAT_ROOT what is installed, so an empty one keeps
#       a host that has installed an SDK (a developer's 10.9 box) from answering "installed"
LONGBOAT_ROOT="$T/root"
export MAVERGREEN_PRODUCT_NAMES MAVERICKS_SDK_URL MAVERICKS_SDK_SHA256 LONGBOAT_ROOT

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

test_list_offers_10_9() {
  [ "$(sh "$LB" sdk list)" = "10.9 sdk109 available" ]
}

test_list_says_installed() {
  mkdir -p "$T/installed/usr/local/mavergreen/sdk109"
  : > "$T/installed/usr/local/mavergreen/sdk109/mavergreen.plist"
  [ "$(LONGBOAT_ROOT="$T/installed" sh "$LB" sdk list)" = "10.9 sdk109 installed" ]
}

test_list_hides_unregistered() {
  [ -z "$(MAVERGREEN_PRODUCT_NAMES="$T/without-sdk109" sh "$LB" sdk list)" ] || return 1
  cat "$T/without-sdk109" "$T/with-sdk109" > "$T/without-plus-sdk109"
  [ "$(MAVERGREEN_PRODUCT_NAMES="$T/without-plus-sdk109" sh "$LB" sdk list)" = "10.9 sdk109 available" ] \
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
    LONGBOAT_SHIPYARD_DIR="$d" sh "$LB" sdk list >"$T/bout" 2>"$T/berr" || rc=$?
    [ "$rc" -ne 0 ] && [ ! -s "$T/bout" ] && [ -s "$T/berr" ] \
      || { echo "list with a broken shipyard dir $d exited $rc and said nothing on stderr" >&2; return 1; }
  done
  rc=0
  MAVERGREEN_PRODUCT_NAMES="$T/no-such-registry" sh "$LB" sdk list >"$T/bout" 2>"$T/berr" || rc=$?
  [ "$rc" -ne 0 ] && [ ! -s "$T/bout" ] && [ -s "$T/berr" ] \
    || { echo "list with an unreadable registry exited $rc" >&2; return 1; }
}

test_fetch_on_broken_shipyard_is_not_unknown() {
  make_broken_shipyards
  rc=0
  LONGBOAT_SHIPYARD_DIR="$T/empty-shipyard" sh "$LB" sdk fetch 10.9 --into "$T/brokenfetch" >/dev/null 2>"$T/berr" || rc=$?
  [ "$rc" -eq 1 ] && [ -s "$T/berr" ] && [ -z "$(grep "no SDK for version" "$T/berr")" ] \
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

# spec: src/longboat lb_sdk, sdk_usage -- the sdk word carries only its own verbs; misuse prints
#       its usage on stderr, exit 2, and runs nothing
test_sdk_without_verb_is_usage() {
  for args in "sdk" "sdk frob" "sdk verify 10.9 x"; do
    rc=0
    # shellcheck disable=SC2086
    sh "$LB" $args >"$T/sout" 2>"$T/serr" || rc=$?
    [ "$rc" -eq 2 ] && [ ! -s "$T/sout" ] && grep -q 'longboat sdk list' "$T/serr" \
      || { echo "longboat $args exited $rc, stdout '$(cat "$T/sout")', stderr '$(cat "$T/serr")'" >&2; return 1; }
  done
}

test_sdk_help_names_only_sdk_verbs() {
  sh "$LB" sdk --help >"$T/shelp" 2>"$T/sherr" || return 1
  [ ! -s "$T/sherr" ] || return 1
  for v in 'longboat sdk list' 'longboat sdk install <version>' 'longboat sdk uninstall <version>' \
      'longboat sdk fetch <version> --into DIR'; do
    grep -qF "$v" "$T/shelp" || { echo "sdk --help does not name '$v'" >&2; return 1; }
  done
  ! grep -q 'media' "$T/shelp" || { echo "sdk --help names media" >&2; return 1; }
}

test_sdk_unknown_version_names_sdk_versions() {
  rc=0
  sh "$LB" sdk fetch 10.6 --into "$T/unk6" >/dev/null 2>"$T/err6" || rc=$?
  [ "$rc" -eq 2 ] && grep -q "longboat: no SDK for version '10.6'; offered: 10.9$" "$T/err6" \
    || { echo "sdk fetch 10.6 exited $rc: $(cat "$T/err6")" >&2; return 1; }
}

test_fetch_extracts() {
  out=$(sh "$LB" sdk fetch 10.9 --into "$T/out")
  [ "$out" = "$T/out/MacOSX10.9.sdk" ] && [ -f "$T/out/MacOSX10.9.sdk/usr/include/stdio.h" ]
}

# spec: shipyard's fetch_sdk.sh (v1.0.544) -- the 10.9 SDK ships only SDKSettings.plist, and swiftc reads the
#       SDK version only from SDKSettings.json, so a fetched 10.9 SDK carries shipyard's, byte for byte, and
#       sdk install copies it into sdk109
sdk_settings_json='{"CanonicalName":"macosx10.9","MaximumDeploymentTarget":"10.9","Version":"10.9"}'

test_fetch_gives_10_9_its_sdk_settings_json() {
  out=$(sh "$LB" sdk fetch 10.9 --into "$T/json") || return 1
  printf '%s\n' "$sdk_settings_json" | cmp - "$out/SDKSettings.json" >&2
}

# spec: src/install.sh lb_install_build -- sdk install fetches into a cache an older longboat may have filled,
#       before SDKSettings.json existed; the fetch must add it there too, or the rebuilt sdk109 lacks it
test_fetch_adds_sdk_settings_json_to_an_old_cache() {
  out=$(sh "$LB" sdk fetch 10.9 --into "$T/oldcache") || return 1
  rm -f "$out/SDKSettings.json"
  out=$(sh "$LB" sdk fetch 10.9 --into "$T/oldcache") || return 1
  printf '%s\n' "$sdk_settings_json" | cmp - "$out/SDKSettings.json" >&2
}

# spec: src/fetch.sh lb_fetch -- sdk fetch opts in to shipyard's SDKSettings.json (MAVERICKS_SDK_SETTINGS_JSON=1)
#       and prints the path fetch_sdk.sh prints, which may be a sibling of the pristine SDK; a stub fetch_sdk.sh
#       around the real one stands in for a shipyard that answers the opt-in that way
test_fetch_opts_in_and_prints_fetch_sdks_path() {
  stub="$T/optin-shipyard"
  rm -rf "$stub"
  cp -R "$LONGBOAT_SHIPYARD_DIR" "$stub"
  cat > "$stub/fetch_sdk.sh" <<EOF
#!/bin/sh
out="\$(sh "$LONGBOAT_SHIPYARD_DIR/fetch_sdk.sh" "\$@")" || exit 1
[ "\${MAVERICKS_SDK_SETTINGS_JSON:-}" = 1 ] || { echo "\$out"; exit 0; }
mkdir -p "\$MAVERICKS_SDK_CACHE/with-sdk-settings-json/MacOSX10.9.sdk"
echo "\$MAVERICKS_SDK_CACHE/with-sdk-settings-json/MacOSX10.9.sdk"
EOF
  out=$(LONGBOAT_SHIPYARD_DIR="$stub" sh "$LB" sdk fetch 10.9 --into "$T/optin") || return 1
  [ "$out" = "$T/optin/with-sdk-settings-json/MacOSX10.9.sdk" ] \
    || { echo "sdk fetch printed '$out', want fetch_sdk.sh's opted-in path" >&2; return 1; }
}

test_fetch_mismatch_leaves_nothing() {
  if MAVERICKS_SDK_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
      sh "$LB" sdk fetch 10.9 --into "$T/bad" >/dev/null 2>&1; then
    return 1
  fi
  [ ! -e "$T/bad/MacOSX10.9.sdk" ] && sh "$LB" sdk fetch 10.9 --into "$T/bad" >/dev/null
}

test_fetch_reuses_cache_offline() {
  cp "$T/served/MacOSX10.9.sdk.tar.xz" "$T/keep.tar.xz"
  sh "$LB" sdk fetch 10.9 --into "$T/cached" >/dev/null
  rm "$T/served/MacOSX10.9.sdk.tar.xz"
  rc=0
  sh "$LB" sdk fetch 10.9 --into "$T/cached" >/dev/null || rc=1
  cp "$T/keep.tar.xz" "$T/served/MacOSX10.9.sdk.tar.xz"
  [ "$rc" -eq 0 ]
}

test_fetch_shows_its_step() {
  rm -rf "$T/step"
  out=$(sh "$LB" sdk fetch 10.9 --into "$T/step" 2>"$T/steperr")
  [ "$out" = "$T/step/MacOSX10.9.sdk" ] || return 1
  grep -q '^\[1/1\] fetching the 10\.9 SDK\.\.\.$' "$T/steperr" \
    && grep -q '^\[1/1\] fetching the 10\.9 SDK: done (' "$T/steperr" \
    || { cat "$T/steperr" >&2; return 1; }
  [ -z "$(grep "$(printf '\r')" "$T/steperr")" ] || { echo "a log must never carry a CR" >&2; return 1; }
  sh "$LB" sdk fetch 10.9 --into "$T/step" 2>"$T/steperr2" >/dev/null
  grep -q '^\[1/1\] fetching the 10\.9 SDK: cached$' "$T/steperr2" || { cat "$T/steperr2" >&2; return 1; }
}

test_fetch_stdout_is_only_the_path_at_a_terminal() {
  [ "$(uname)" = Darwin ] || { echo "skip: needs BSD script(1)" >&2; return 0; }
  rm -rf "$T/tty"
  # platform: BSD script(1) gives the command a terminal on stderr; GNU script has other flags.
  script -q /dev/null sh -c 'p="$(sh "$1" sdk fetch 10.9 --into "$2")"; printf "PATH=%s\n" "$p"' sh "$LB" "$T/tty" >"$T/ttyout" 2>&1 || true
  tr -d '\r' < "$T/ttyout" > "$T/ttyout.clean"
  grep -qx "PATH=$T/tty/MacOSX10.9.sdk" "$T/ttyout.clean" || { cat "$T/ttyout.clean" >&2; return 1; }
  # spec: src/progress.sh lb_step_end -- only at a terminal is the step's line redrawn with a CR; in a log
  #       script(1)'s CR comes just before a newline, never before the next line's text.
  grep -q "$(printf '\r')\\[1/1\\] fetching the 10\\.9 SDK: done (" "$T/ttyout" \
    || { echo "stderr was a terminal but the step was not redrawn:" >&2; cat -v "$T/ttyout" >&2; return 1; }
}

test_unknown_sdk() {
  for v in 10.6 10.9.5 MacOSX10.9.sdk ""; do
    rc=0
    sh "$LB" sdk fetch "$v" --into "$T/unk" 2>"$T/err" >/dev/null || rc=$?
    [ "$rc" -eq 2 ] && grep -q '10\.9' "$T/err" || return 1
  done
}

test_paths_with_spaces() {
  mkdir -p "$T/tmp space"
  out=$(TMPDIR="$T/tmp space" sh "$LB" sdk fetch 10.9 --into "$T/with space")
  [ "$out" = "$T/with space/MacOSX10.9.sdk" ] && [ -f "$out/usr/include/stdio.h" ]
}

test_runs_through_symlink() {
  mkdir -p "$T/farm/bin" "$T/farm/longboat/bin"
  cp "$LB" "$T/farm/longboat/bin/longboat"
  mkdir -p "$T/farm/longboat/libexec/longboat"
  cp "$root"/src/sdks.sh "$root"/src/progress.sh "$root"/src/fetch.sh "$root"/src/install.sh "$root"/src/uninstall.sh "$T/farm/longboat/libexec/longboat/"
  ln -s ../longboat/bin/longboat "$T/farm/bin/longboat"
  sh "$T/farm/bin/longboat" --help >/dev/null 2>&1 && sh "$T/farm/bin/longboat" sdk list | grep -qx '10.9 sdk109 available'
}

for t in test_sdk_without_verb_is_usage test_sdk_help_names_only_sdk_verbs test_sdk_unknown_version_names_sdk_versions test_list_offers_10_9 test_list_says_installed test_list_hides_unregistered test_list_fails_on_broken_shipyard \
    test_fetch_on_broken_shipyard_is_not_unknown test_help_on_stdout test_usage_error_on_stderr \
    test_fetch_extracts test_fetch_gives_10_9_its_sdk_settings_json test_fetch_adds_sdk_settings_json_to_an_old_cache \
    test_fetch_opts_in_and_prints_fetch_sdks_path \
    test_fetch_mismatch_leaves_nothing test_fetch_reuses_cache_offline \
    test_fetch_shows_its_step test_fetch_stdout_is_only_the_path_at_a_terminal test_unknown_sdk test_paths_with_spaces test_runs_through_symlink; do
  check "$t"
done
exit "$fail"
