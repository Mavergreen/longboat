#!/bin/sh
# platform: macOS-only -- builds a Cocoa app with Apple's toolchain
#   usage: build-updater.sh     prints the path of the universal longboat-updater.app
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
[ -s "$ROOT/VERSION" ] || {
  echo "build-updater: no VERSION file; CI's ver job writes it, and without one CMake would write a -mavericks.N VERSION here" >&2
  exit 1
}
MAVERICKS_ROOT="$ROOT"; export MAVERICKS_ROOT
. "$ROOT/build/msc.sh"
BUILD_ROOT="${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}"
# platform: /usr/local/mavergreen/bin reaches PATH only through a login shell's paths.d
SYCM="$(command -v shipyard-cmake || echo /usr/local/mavergreen/bin/shipyard-cmake)"
for pair in "x86_64" "arm64"; do
  B="$BUILD_ROOT/mavergreen-longboat-updater-$pair"
  # platform: a pkgsrc or Homebrew clang can precede /usr/bin on PATH, and RequireAppleClang refuses it
  OBJC=/usr/bin/clang "$SYCM" -S "$ROOT" -B "$B" -DLONGBOAT_ARCH="$pair" \
    -DCMAKE_TOOLCHAIN_FILE="$(dirname "$SHIPYARD_SCRIPTS")/MavericksToolchain.cmake" >&2
  "$SYCM" --build "$B" >&2
  [ -d "$B/longboat-updater.app" ] || { echo "FATAL: no longboat-updater.app under $B" >&2; exit 1; }
done
out="$BUILD_ROOT/longboat-updater.app"
rm -rf "$out"
sh "$SHIPYARD_SCRIPTS/lipo-merge-tree.sh" \
  --a "$BUILD_ROOT/mavergreen-longboat-updater-x86_64/longboat-updater.app" \
  --b "$BUILD_ROOT/mavergreen-longboat-updater-arm64/longboat-updater.app" \
  --out "$out" --require-archs "x86_64 arm64" >&2
echo "$out" > "$BUILD_ROOT/longboat-updater.path"
echo "$out"
