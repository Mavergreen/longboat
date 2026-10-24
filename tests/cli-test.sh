#!/bin/sh
# platform: host-agnostic
# spec: src/longboat lb_list, usage, lb_moved -- longboat list is one table of both jobs by version;
#       --help names both words; each old form runs as its new form after one stderr line saying so,
#       which never carries anything but the command words and the version
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-cli.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

# spec: build/package.sh's installed layout, with a stub installmedia: list prints $T/media-list, which starts
#       as the two lines the real one prints today, and anything else records its argv and exits 7
mkdir -p "$T/lb/bin" "$T/lb/libexec/longboat"
cp "$root/src/longboat" "$T/lb/bin/longboat"
for f in sdks.sh progress.sh fetch.sh install.sh uninstall.sh; do
  cp "$root/src/$f" "$T/lb/libexec/longboat/$f"
done
cat > "$T/lb/libexec/longboat/installmedia" <<EOF
#!/bin/sh
[ "\$*" = list ] && { cat "$T/media-list"; exit 0; }
for a; do printf '%s\n' "\$a"; done > "$T/argv"
exit 7
EOF
chmod 755 "$T/lb/libexec/longboat/installmedia"
printf '10.6 bring-your-own\n10.9 recipe\n' > "$T/media-list"
LB="$T/lb/bin/longboat"

mkdir -p "$T/src/MacOSX10.9.sdk/usr/include" "$T/src/MacOSX10.9.sdk/usr/lib" "$T/served" "$T/linux"
echo "int x;" > "$T/src/MacOSX10.9.sdk/usr/include/stdio.h"
COPYFILE_DISABLE=1 tar cJf "$T/served/MacOSX10.9.sdk.tar.xz" -C "$T/src" MacOSX10.9.sdk
printf 'sdk109 dev.mavergreen.longboat.sdk109 longboat\nsdk113 dev.mavergreen.longboat.sdk113 longboat\n' > "$T/registry"
echo "longboat dev.mavergreen.longboat longboat" > "$T/no-sdks"
mkdir -p "$T/root/usr/local/mavergreen/sdk109"
: > "$T/root/usr/local/mavergreen/sdk109/mavergreen.plist"
printf '#!/bin/sh\necho Linux\n' > "$T/linux/uname"
chmod 755 "$T/linux/uname"
LONGBOAT_SHIPYARD_DIR="${LONGBOAT_SHIPYARD_DIR:-$SHIPYARD_SCRIPTS}"
MAVERGREEN_PRODUCT_NAMES="$T/registry"
MAVERICKS_SDK_URL="file://$T/served/MacOSX10.9.sdk.tar.xz"
MAVERICKS_SDK_SHA256=$(shasum -a 256 < "$T/served/MacOSX10.9.sdk.tar.xz" | cut -c1-64)
LONGBOAT_ROOT="$T/root"
export LONGBOAT_SHIPYARD_DIR MAVERGREEN_PRODUCT_NAMES MAVERICKS_SDK_URL MAVERICKS_SDK_SHA256 LONGBOAT_ROOT

lb() {
  rm -f "$T/argv"
  rc=0
  sh "$LB" "$@" >"$T/out" 2>"$T/err" || rc=$?
}

test_list_table() {
  lb list
  printf '%s\n' 'version  sdk        media' '10.6     -          bring your own disc' \
    '10.9     installed  available' '11.3     available  -' > "$T/want"
  [ "$rc" -eq 0 ] && [ ! -s "$T/err" ] && diff "$T/want" "$T/out" >&2
}

test_list_without_sdks_shows_media() {
  rc=0
  MAVERGREEN_PRODUCT_NAMES="$T/no-sdks" sh "$LB" list >"$T/out" 2>"$T/err" || rc=$?
  printf '%s\n' 'version  sdk        media' '10.6     -          bring your own disc' '10.9     -          available' > "$T/want"
  [ "$rc" -eq 0 ] && diff "$T/want" "$T/out" >&2
}

test_list_fails_when_installmedia_is_missing() {
  mv "$T/lb/libexec/longboat/installmedia" "$T/installmedia.away"
  lb list
  mv "$T/installmedia.away" "$T/lb/libexec/longboat/installmedia"
  [ "$rc" -eq 1 ] && [ ! -s "$T/out" ] && grep -q installmedia "$T/err" \
    || { echo "list without installmedia exited $rc: $(cat "$T/err")" >&2; return 1; }
}

test_list_fails_on_a_broken_shipyard_copy() {
  mkdir -p "$T/empty-shipyard"
  rc=0
  LONGBOAT_SHIPYARD_DIR="$T/empty-shipyard" sh "$LB" list >"$T/out" 2>"$T/err" || rc=$?
  [ "$rc" -eq 1 ] && [ ! -s "$T/out" ] && [ -s "$T/err" ] \
    || { echo "list with an empty shipyard copy exited $rc, stdout '$(cat "$T/out")'" >&2; return 1; }
}

# spec: src/longboat lb_list -- a media kind longboat does not know is a broken install, not a disc to bring
test_list_refuses_an_unknown_media_kind() {
  printf '10.6 bring-your-own\n10.7 frob\n' > "$T/media-list"
  lb list
  printf '10.6 bring-your-own\n10.9 recipe\n' > "$T/media-list"
  [ "$rc" -eq 1 ] && [ ! -s "$T/out" ] && grep -q "unknown media kind 'frob' for 10.7" "$T/err" \
    || { echo "list with an unknown media kind exited $rc, stdout '$(cat "$T/out")', stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_list_takes_no_arguments() {
  lb list 10.9
  [ "$rc" -eq 2 ] && [ ! -s "$T/out" ]
}

test_help_names_both_words() {
  lb --help
  [ "$rc" -eq 0 ] && [ ! -s "$T/err" ] || return 1
  for want in 'longboat list' 'longboat sdk install <version>' 'longboat sdk uninstall <version>' \
      'longboat media fetch <version> --into DIR' 'longboat media verify <version> <image>' 'man longboat'; do
    grep -qF "$want" "$T/out" || { echo "--help does not name '$want'" >&2; return 1; }
  done
  ! grep -q 'installer' "$T/out" || { echo "--help still names an old form" >&2; return 1; }
}

# spec: src/longboat -- longboat --help <word> is longboat <word> --help; any other second word is usage
test_help_takes_a_word() {
  lb --help sdk
  [ "$rc" -eq 0 ] && grep -q 'usage: longboat sdk list' "$T/out" && ! grep -q 'longboat media' "$T/out" \
    || { echo "--help sdk exited $rc: $(cat "$T/out")" >&2; return 1; }
  lb --help media
  [ "$rc" -eq 7 ] && [ "$(cat "$T/argv")" = --help ] \
    || { echo "--help media exited $rc, want installmedia --help's 7" >&2; return 1; }
  for form in "--help bogus" "--help sdk x" "-h list"; do
    # shellcheck disable=SC2086
    lb $form
    [ "$rc" -eq 2 ] && [ ! -s "$T/out" ] || { echo "longboat $form exited $rc" >&2; return 1; }
  done
}

test_no_command_is_usage() {
  for form in "" "bogus" "verify sdk 10.9"; do
    # shellcheck disable=SC2086
    lb $form
    [ "$rc" -eq 2 ] && [ ! -s "$T/out" ] && grep -q 'longboat sdk list' "$T/err" && grep -q 'longboat media list' "$T/err" \
      || { echo "longboat $form exited $rc: $(cat "$T/err")" >&2; return 1; }
  done
}

test_old_fetch_announces_and_keeps_stdout() {
  lb fetch 10.9 --into "$T/old"
  [ "$rc" -eq 0 ] && [ "$(cat "$T/out")" = "$T/old/MacOSX10.9.sdk" ] \
    && [ "$(sed -n 1p "$T/err")" = "longboat: 'longboat fetch 10.9' is now 'longboat sdk fetch 10.9'" ] \
    || { echo "exit $rc, stdout '$(cat "$T/out")', stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_old_fetch_installer_withholds_the_url() {
  lb fetch installer 10.9 --into "$T/D" --source-url https://secret.example/x
  [ "$rc" -eq 7 ] && [ "$(sed -n 1p "$T/err")" = "longboat: 'longboat fetch installer 10.9' is now 'longboat media fetch 10.9'" ] \
    && ! grep -q secret "$T/err" \
    && [ "$(tr '\n' ' ' < "$T/argv")" = "fetch 10.9 --into $T/D --source-url https://secret.example/x " ] \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_old_form_never_echoes_a_secret_in_the_version_place() {
  lb fetch installer https://secret.example/x --into "$T/D"
  [ "$(sed -n 1p "$T/err")" = "longboat: 'longboat fetch installer' is now 'longboat media fetch'" ] && ! grep -q secret "$T/err" \
    || { echo "stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_old_verify_installer_announces() {
  lb verify installer 10.6 x
  [ "$rc" -eq 7 ] && [ "$(sed -n 1p "$T/err")" = "longboat: 'longboat verify installer 10.6' is now 'longboat media verify 10.6'" ] \
    && [ "$(tr '\n' ' ' < "$T/argv")" = "verify 10.6 x " ]
}

test_old_install_announces_then_refuses_off_macos() {
  rc=0
  PATH="$T/linux:$PATH" sh "$LB" install 10.9 >"$T/out" 2>"$T/err" || rc=$?
  [ "$rc" -eq 2 ] && [ "$(sed -n 1p "$T/err")" = "longboat: 'longboat install 10.9' is now 'longboat sdk install 10.9'" ] \
    && [ "$(sed -n 2p "$T/err")" = "longboat sdk install needs macOS for now: it builds and installs a .pkg" ] \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

for t in test_list_table test_list_without_sdks_shows_media test_list_fails_when_installmedia_is_missing \
    test_list_fails_on_a_broken_shipyard_copy test_list_refuses_an_unknown_media_kind \
    test_list_takes_no_arguments test_help_names_both_words test_help_takes_a_word test_no_command_is_usage \
    test_old_fetch_announces_and_keeps_stdout test_old_fetch_installer_withholds_the_url \
    test_old_form_never_echoes_a_secret_in_the_version_place test_old_verify_installer_announces \
    test_old_install_announces_then_refuses_off_macos; do
  check "$t"
done
exit "$fail"
