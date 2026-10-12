#!/bin/sh
# platform: macOS-only -- runs build/build-updater.sh, which builds a Cocoa app with Apple's toolchain
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"
export SHIPYARD_SCRIPTS

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-build-updater.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0
R="$T/repo copy"
mkdir -p "$R/build" "$T/bin" "$T/mm build"
cp "$root"/build/*.sh "$R/build/"
cp "$root/CMakeLists.txt" "$R/"
cat > "$T/bin/shipyard-cmake" <<EOF
#!/bin/sh
echo called >> "$T/cmake-called"
exit 1
EOF
chmod +x "$T/bin/shipyard-cmake"

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

run_build_updater() {
  rm -f "$T/cmake-called"
  rc=0
  PATH="$T/bin:$PATH" MAVERICKS_BUILD_ROOT="$T/mm build" sh "$R/build/build-updater.sh" >"$T/out" 2>"$T/err" || rc=$?
}

test_refuses_without_version() {
  rm -f "$R/VERSION"
  run_build_updater
  [ "$rc" -ne 0 ] || { echo "build-updater.sh succeeded with no VERSION" >&2; return 1; }
  grep -q 'VERSION' "$T/err" || { echo "the refusal must name VERSION; stderr: $(cat "$T/err")" >&2; return 1; }
  [ ! -e "$T/cmake-called" ] \
    || { echo "build-updater.sh configured with no VERSION: CMake's mavericks_resolve_version would write a -mavericks.N VERSION into the source tree" >&2; return 1; }
  [ ! -e "$R/VERSION" ] || { echo "build-updater.sh wrote $R/VERSION" >&2; return 1; }
}

test_configures_with_version() {
  printf '%s\n' 20990101.1 > "$R/VERSION"
  run_build_updater
  rm -f "$R/VERSION"
  [ -e "$T/cmake-called" ] || { echo "with a VERSION, build-updater.sh must reach shipyard-cmake; stderr: $(cat "$T/err")" >&2; return 1; }
}

check test_refuses_without_version
check test_configures_with_version
exit "$fail"
