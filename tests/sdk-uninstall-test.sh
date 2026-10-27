#!/bin/sh
# platform: host-agnostic
# spec: src/uninstall.sh lb_uninstall -- on a Mac it runs the installed mavergreen helper's
#       uninstall for the version's short name; it needs root, refuses an unknown or uninstalled version,
#       and refuses off macOS. Stub id, uname and helper stand in, so nothing here needs root.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
LB="$root/src/longboat"
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"
LONGBOAT_SHIPYARD_DIR="${LONGBOAT_SHIPYARD_DIR:-$SHIPYARD_SCRIPTS}"
export LONGBOAT_SHIPYARD_DIR

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-sdk-uninstall.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

echo "sdk109 dev.mavergreen.longboat.sdk109 longboat" > "$T/registry"
MAVERGREEN_PRODUCT_NAMES="$T/registry"
LONGBOAT_ROOT="$T/root"
export MAVERGREEN_PRODUCT_NAMES LONGBOAT_ROOT
mkdir -p "$T/root/usr/local/bin" "$T/as-root" "$T/linux" "$T/darwin"
cat > "$T/root/usr/local/bin/mavergreen" <<EOS
#!/bin/sh
printf '%s\n' "\$*" > "$T/helper.argv"
EOS
printf '#!/bin/sh\necho 0\n' > "$T/as-root/id"
printf '#!/bin/sh\necho Linux\n' > "$T/linux/uname"
printf '#!/bin/sh\necho Darwin\n' > "$T/darwin/uname"
chmod 755 "$T/root/usr/local/bin/mavergreen" "$T/as-root/id" "$T/linux/uname" "$T/darwin/uname"

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

MG="$T/root/usr/local/mavergreen"
installed() { mkdir -p "$MG/sdk109/SDKs/MacOSX10.9.sdk" && : > "$MG/sdk109/mavergreen.plist"; }
not_installed() { rm -rf "$MG/sdk109"; }
# product <name> <link target>: an installed product whose SDKs is a link to <link target>, as clang-22's is
product() { mkdir -p "$MG/$1" && : > "$MG/$1/mavergreen.plist" && rm -f "$MG/$1/SDKs" && ln -s "$2" "$MG/$1/SDKs"; }

# platform: the macOS cases stand in a Darwin uname after $1, so they run on a Linux host too, and a Linux uname
#           given in $1 still wins
uninstall() {
  rm -f "$T/helper.argv"
  rc=0
  PATH="$1:$T/darwin:$PATH" sh "$LB" sdk uninstall "$2" >"$T/out" 2>"$T/err" || rc=$?
}

test_uninstall_runs_the_installed_helper() {
  installed
  uninstall "$T/as-root" 10.9
  [ "$rc" -eq 0 ] && [ "$(cat "$T/helper.argv" 2>/dev/null)" = "uninstall sdk109" ] \
    || { echo "exit $rc, helper argv '$(cat "$T/helper.argv" 2>/dev/null)', stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_uninstall_not_installed_exits_1() {
  not_installed
  uninstall "$T/as-root" 10.9
  [ "$rc" -eq 1 ] && [ ! -f "$T/helper.argv" ] && grep -q "the 10.9 SDK is not installed" "$T/err" \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_uninstall_unknown_version_exits_2() {
  installed
  uninstall "$T/as-root" 10.6
  [ "$rc" -eq 2 ] && [ ! -f "$T/helper.argv" ] && grep -q "offered: 10.9" "$T/err" \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_uninstall_needs_root() {
  [ "$(id -u)" -ne 0 ] || { echo "skip: running as root" >&2; return 0; }
  installed
  uninstall /nonexistent 10.9
  [ "$rc" -eq 2 ] && [ ! -f "$T/helper.argv" ] && grep -q 'sudo longboat sdk uninstall 10.9' "$T/err" \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_uninstall_off_macos_is_refused() {
  installed
  uninstall "$T/linux:$T/as-root" 10.9
  [ "$rc" -eq 2 ] && [ ! -f "$T/helper.argv" ] \
    && grep -qx 'longboat sdk uninstall needs macOS for now: it builds and installs a .pkg' "$T/err" \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

# spec: src/uninstall.sh lb_sdk_dependents -- clang-22's and swift's toolchains link their SDKs into sdk109
#       (shipyard's conventions skill), so uninstalling it would leave a link that fails later, in a build; it
#       refuses while any installed product's tree has a link resolving into the SDK, naming each product and
#       how to remove it, and removes nothing
test_uninstall_refuses_while_a_product_links_in() {
  installed
  product clang-22 ../sdk109/SDKs
  product swift /usr/local/mavergreen/sdk109/SDKs/MacOSX10.9.sdk
  uninstall "$T/as-root" 10.9
  rm -rf "$MG/clang-22" "$MG/swift"
  [ "$rc" -eq 1 ] && [ ! -f "$T/helper.argv" ] || { echo "exit $rc, helper argv '$(cat "$T/helper.argv" 2>/dev/null)'" >&2; return 1; }
  printf '%s\n' "longboat: the 10.9 SDK is in use: these products link into sdk109, so remove them first:" \
    "  sudo mavergreen uninstall clang-22" "  sudo mavergreen uninstall swift" | cmp - "$T/err" >&2 \
    || { cat "$T/err" >&2; return 1; }
}

# spec: src/uninstall.sh lb_sdk_dependents -- only an installed product's link into the SDK holds it: a link
#       elsewhere, a dangling link, and a link from a tree that is not an installed product do not
test_uninstall_ignores_other_links() {
  installed
  mkdir -p "$MG/other/lib" "$MG/var/longboat"
  product clang-22 ../other/lib
  ln -s ../nowhere "$MG/clang-22/dangling"
  ln -s ../../sdk109/SDKs "$MG/var/longboat/SDKs"
  uninstall "$T/as-root" 10.9
  rm -rf "$MG/clang-22" "$MG/other" "$MG/var"
  [ "$rc" -eq 0 ] && [ "$(cat "$T/helper.argv" 2>/dev/null)" = "uninstall sdk109" ] \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

test_uninstall_without_helper_says_so() {
  installed
  mv "$T/root/usr/local/bin/mavergreen" "$T/helper.away"
  uninstall "$T/as-root" 10.9
  mv "$T/helper.away" "$T/root/usr/local/bin/mavergreen"
  [ "$rc" -eq 1 ] && [ "$(cat "$T/err")" = "longboat: no mavergreen helper at $T/root/usr/local/bin/mavergreen; reinstall Longboat's pkg, which carries it" ] \
    || { echo "exit $rc, stderr '$(cat "$T/err")'" >&2; return 1; }
}

for t in test_uninstall_runs_the_installed_helper test_uninstall_not_installed_exits_1 \
    test_uninstall_unknown_version_exits_2 test_uninstall_needs_root test_uninstall_off_macos_is_refused \
    test_uninstall_without_helper_says_so test_uninstall_refuses_while_a_product_links_in \
    test_uninstall_ignores_other_links; do
  check "$t"
done
exit "$fail"
