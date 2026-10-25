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

built() { [ -f "$T/dist/sdk109-20261012.1.pkg" ] || sh "$LB" sdk install 10.9 --pkg-only "$T/dist" >/dev/null; }

test_install_needs_root() {
  [ "$(id -u)" -ne 0 ] || { echo "skip: running as root" >&2; return 0; }
  rc=0
  sh "$LB" sdk install 10.9 2>"$T/err" >/dev/null || rc=$?
  [ "$rc" -eq 2 ] && grep -q 'sudo longboat sdk install 10.9' "$T/err" && [ ! -e "$T/root" ]
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

# spec: src/install.sh lb_install_copy -- sdk109 carries shipyard's SDKSettings.json as a real file even when
#       fetch_sdk.sh leaves the cached SDK pristine (its default once the JSON is opt-in), and the install never
#       opts in, since the opted-in SDK is symlinks into the cache; a stub fetch_sdk.sh around the real one
#       strips the JSON and records the opt-in
test_pkg_carries_sdk_settings_json_from_a_pristine_sdk() {
  stub="$T/pristine-shipyard"
  rm -rf "$stub" "$T/json-dist" "$T/json-cpio" "$T/json-expanded" "$T/json-payload"
  cp -R "$LONGBOAT_SHIPYARD_DIR" "$stub"
  cat > "$stub/fetch_sdk.sh" <<EOF
#!/bin/sh
echo "\${MAVERICKS_SDK_SETTINGS_JSON:-unset}" > "$T/optin-seen"
out="\$(sh "$LONGBOAT_SHIPYARD_DIR/fetch_sdk.sh" "\$@")" || exit 1
rm -f "\$out/SDKSettings.json"
echo "\$out"
EOF
  LONGBOAT_SHIPYARD_DIR="$stub" sh "$LB" sdk install 10.9 --pkg-only "$T/json-dist" >/dev/null || return 1
  [ "$(cat "$T/optin-seen")" = unset ] || { echo "the install opted in to the JSON sibling" >&2; return 1; }
  pkgutil --expand "$T/json-dist/sdk109-20261012.1.pkg" "$T/json-expanded" || return 1
  comp="$(grep -l 'dev\.mavergreen\.longboat\.sdk109' "$T"/json-expanded/*.pkg/PackageInfo | head -1)"
  gzip -dc "$(dirname "$comp")/Payload" > "$T/json-cpio" || return 1
  links="$(cpio -itv < "$T/json-cpio" 2>/dev/null | grep '^l' || true)"
  [ -z "$links" ] || { echo "the sdk109 payload has symlinks: $links" >&2; return 1; }
  mkdir -p "$T/json-payload"
  f=./usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk/SDKSettings.json
  ( cd "$T/json-payload" && cpio -id "$f" < "$T/json-cpio" 2>/dev/null ) || return 1
  printf '%s\n' '{"CanonicalName":"macosx10.9","MaximumDeploymentTarget":"10.9","Version":"10.9"}' \
    | cmp - "$T/json-payload/$f" >&2
}

test_pkg_only_shows_four_steps() {
  rm -rf "$T/sdist"
  out="$(sh "$LB" sdk install 10.9 --pkg-only "$T/sdist" 2>"$T/steperr")" || { cat "$T/steperr" >&2; return 1; }
  [ "$out" = "$T/sdist/sdk109-20261012.1.pkg" ] || { echo "stdout was '$out'" >&2; return 1; }
  prev=0
  for s in "1 fetching the 10.9 SDK" "2 copying the SDK" "3 building the SDK pkg" "4 adding the install floor"; do
    n="${s%% *}" label="${s#* }"
    a="$(grep -n "^\[$n/4\] $label\.\.\.\$" "$T/steperr" | head -1 | cut -d: -f1)"
    b="$(grep -n "^\[$n/4\] $label: done (" "$T/steperr" | head -1 | cut -d: -f1)"
    [ -n "$a" ] && [ -n "$b" ] && [ "$a" -gt "$prev" ] && [ "$b" -gt "$a" ] \
      || { echo "step $n: start line $a, done line $b, after line $prev" >&2; cat "$T/steperr" >&2; return 1; }
    prev="$b"
  done
  ! grep -q "^\[5/" "$T/steperr" || false
}

test_failing_step_shows_its_output() {
  rm -rf "$T/badsy" "$T/fdist2"
  cp -R "$LONGBOAT_SHIPYARD_DIR" "$T/badsy"
  printf 'echo boom >&2\nexit 1\n' > "$T/badsy/build_component_pkg.sh"
  rc=0
  LONGBOAT_SHIPYARD_DIR="$T/badsy" sh "$LB" sdk install 10.9 --pkg-only "$T/fdist2" >/dev/null 2>"$T/steperr" || rc=$?
  [ "$rc" -ne 0 ] || return 1
  grep -q '^\[3/4\] building the SDK pkg: failed (' "$T/steperr" || { cat "$T/steperr" >&2; return 1; }
  sed -n '/^\[3\/4\] building the SDK pkg: failed (/,$p' "$T/steperr" | grep -qx boom \
    || { echo "boom did not follow the failed line" >&2; cat "$T/steperr" >&2; return 1; }
  [ -z "$(ls -A "$T/tmp space")" ]
}

# platform: PATH shims stand in for id and installer, so the real install path runs without root and never touches /.
make_installer_shims() {
  rm -rf "$T/shims" "$T/shimlog"
  mkdir -p "$T/shims"
  printf '#!/bin/sh\n[ "${1:-}" = -u ] && { echo 0; exit 0; }\nexec /usr/bin/id "$@"\n' > "$T/shims/id"
  {
    printf '#!/bin/sh\necho "$@" >> "%s"\n' "$T/shimlog"
    printf 'echo "installer: Package name is shim"\necho "installer:%%50.000000"\n'
    if [ "$1" = fail ]; then
      printf 'echo "installer: The install failed (The Installer encountered an error)" >&2\nexit 1\n'
    else
      printf 'echo "installer:%%100.000000"\nexit 0\n'
    fi
  } > "$T/shims/installer"
  chmod +x "$T/shims/id" "$T/shims/installer"
}

test_installer_failure_fails_install() {
  make_installer_shims fail
  rc=0
  PATH="$T/shims:$PATH" sh "$LB" sdk install 10.9 >"$T/iout" 2>"$T/ierr" || rc=$?
  [ "$rc" -ne 0 ] && [ ! -s "$T/iout" ] \
    && grep -q '^installer: The install failed' "$T/ierr" \
    && grep -q '^\[5/5\] installing: failed (' "$T/ierr" \
    && [ -s "$T/shimlog" ] || { echo "rc=$rc"; cat "$T/iout" "$T/ierr" >&2; return 1; }
  [ -z "$(ls -A "$T/tmp space")" ]
}

test_installer_success_prints_installed_path() {
  make_installer_shims ok
  rc=0
  PATH="$T/shims:$PATH" sh "$LB" sdk install 10.9 >"$T/iout" 2>"$T/ierr" || rc=$?
  [ "$rc" -eq 0 ] && [ "$(cat "$T/iout")" = /usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk ] \
    && grep -q '^\[5/5\] installing: done (' "$T/ierr" \
    && grep -q -e '-verboseR' "$T/shimlog" && grep -q -e '-target /' "$T/shimlog" || { echo "rc=$rc"; cat "$T/iout" "$T/ierr" >&2; return 1; }
}

test_home_untouched() {
  mkdir -p "$T/home"
  rm -rf "$T/dist2"
  HOME="$T/home" sh "$LB" sdk install 10.9 --pkg-only "$T/dist2" >/dev/null || return 1
  [ -z "$(ls -A "$T/home")" ]
}

test_install_failure_leaves_nothing() {
  mkdir -p "$T/fdist"
  echo old > "$T/fdist/sdk109-old.pkg"
  rc=0
  MAVERICKS_SDK_SHA256=0000000000000000000000000000000000000000000000000000000000000000 \
    sh "$LB" sdk install 10.9 --pkg-only "$T/fdist" >/dev/null 2>&1 || rc=$?
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
    sh "$LB" sdk install 10.9 >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ] || return 1
  [ "$(cat "$sdk/mavergreen.plist")" = old ] && [ -d "$sdk/SDKs/MacOSX10.9.sdk" ] || return 1
  [ -z "$(ls -A "$T/root/usr/local/mavergreen/var/longboat" 2>/dev/null)" ] || return 1
  [ -z "$(ls -A "$T/tmp space")" ]
}

test_missing_version_exits_2() {
  rc=0
  env -u LONGBOAT_VERSION sh "$LB" sdk install 10.9 --pkg-only "$T/nover" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 2 ]
}


# spec: Longboat design decision 4 -- sdk113 installs at /usr/local/mavergreen/sdk113/SDKs/MacOSX11.3.sdk,
#       offered once the copied registry names it; Apple called the OS "macOS" from 10.12 on
test_sdk_name() {
  ( . "$root/src/install.sh"
    for pair in "10.9=Mac OS X 10.9" "10.11=Mac OS X 10.11" "10.12=macOS 10.12" "11.3=macOS 11.3"; do
      got="$(lb_sdk_name "${pair%%=*}")"
      [ "$got" = "${pair#*=}" ] || { echo "lb_sdk_name ${pair%%=*} printed '$got', want '${pair#*=}'" >&2; exit 1; }
    done )
}

dist_title() {
  rm -rf "$T/title"
  pkgutil --expand "$1" "$T/title" || return 1
  sed -n 's:.*<title>\(.*\)</title>.*:\1:p' "$T/title/Distribution"
}

test_pkg_title_10_9() {
  built || return 1
  got="$(dist_title "$T/dist/sdk109-20261012.1.pkg")" || return 1
  [ "$got" = "Mac OS X 10.9 SDK (installed by Longboat)" ] || { echo "the 10.9 pkg's title is '$got'" >&2; return 1; }
}

test_pkg_only_builds_11_3() {
  mkdir -p "$T/src113/MacOSX11.3.sdk/usr/include" "$T/src113/MacOSX11.3.sdk/usr/lib"
  echo "int y;" > "$T/src113/MacOSX11.3.sdk/usr/include/stdio.h"
  COPYFILE_DISABLE=1 tar cJf "$T/served/MacOSX11.3.sdk.tar.xz" -C "$T/src113" MacOSX11.3.sdk
  sha="$(shasum -a 256 < "$T/served/MacOSX11.3.sdk.tar.xz" | cut -c1-64)"
  { cat "$T/with-sdk109"; echo "sdk113 dev.mavergreen.longboat.sdk113 longboat"; } > "$T/with-sdk113"
  out="$(MAVERGREEN_PRODUCT_NAMES="$T/with-sdk113" MAVERICKS_SDK_URL="file://$T/served/MacOSX11.3.sdk.tar.xz" \
    MAVERICKS_SDK_SHA256="$sha" sh "$LB" sdk install 11.3 --pkg-only "$T/dist113" 2>"$T/err113")" \
    || { cat "$T/err113" >&2; return 1; }
  pkg="$T/dist113/sdk113-20261012.1.pkg"
  [ "$out" = "$pkg" ] || { echo "install 11.3 --pkg-only printed '$out', want $pkg" >&2; return 1; }
  got="$(dist_title "$pkg")" || return 1
  [ "$got" = "macOS 11.3 SDK (installed by Longboat)" ] || { echo "the 11.3 pkg's title is '$got'" >&2; return 1; }
  comp="$(grep -l 'dev\.mavergreen\.longboat\.sdk113' "$T"/title/*.pkg/PackageInfo | head -1)"
  [ -n "$comp" ] || { echo "no component PackageInfo names dev.mavergreen.longboat.sdk113" >&2; return 1; }
  gzip -dc "$(dirname "$comp")/Payload" | cpio -it 2>/dev/null \
    | grep -qx './usr/local/mavergreen/sdk113/SDKs/MacOSX11.3.sdk/usr/include/stdio.h' \
    || { echo "the sdk113 payload lacks MacOSX11.3.sdk's stdio.h" >&2; return 1; }
}

# spec: src/longboat lb_moved -- the old form announces itself on stderr and leaves stdout exactly as the
#       new form prints it, so a caller capturing the path gets only the path
test_old_install_form_keeps_stdout() {
  new="$(sh "$LB" sdk install 10.9 --pkg-only "$T/newform" 2>/dev/null)" || return 1
  old="$(sh "$LB" install 10.9 --pkg-only "$T/oldform" 2>"$T/olderr")" || { cat "$T/olderr" >&2; return 1; }
  [ "$old" = "$T/oldform/sdk109-20261012.1.pkg" ] && [ "$new" = "$T/newform/sdk109-20261012.1.pkg" ] \
    && [ "$(sed -n 1p "$T/olderr")" = "longboat: 'longboat install 10.9' is now 'longboat sdk install 10.9'" ] \
    || { echo "old stdout '$old', new stdout '$new', old stderr '$(sed -n 1p "$T/olderr")'" >&2; return 1; }
}

for t in test_install_needs_root test_pkg_only_builds_conformant_pkg test_pkg_payload test_pkg_carries_sdk_settings_json_from_a_pristine_sdk \
    test_pkg_only_shows_four_steps test_failing_step_shows_its_output \
    test_installer_failure_fails_install test_installer_success_prints_installed_path \
    test_home_untouched test_install_failure_leaves_nothing test_root_install_failure_leaves_installed_sdk test_missing_version_exits_2 \
    test_sdk_name test_pkg_title_10_9 test_pkg_only_builds_11_3 test_old_install_form_keeps_stdout; do
  check "$t"
done
exit "$fail"
