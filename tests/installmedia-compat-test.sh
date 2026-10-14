#!/bin/sh
# platform: macOS-only -- reads Mach-O with otool and lipo through assert_binary_compatible.sh
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"
bin="${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}/installmedia/darwin-x86_64/installmedia"
[ -f "$bin" ] || { echo "SKIP: no $bin; run build/build-installmedia.sh darwin $bin"; exit 77; }
sh "$SHIPYARD_SCRIPTS/assert_binary_compatible.sh" "$bin"
