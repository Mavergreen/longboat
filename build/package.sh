#!/bin/sh
# platform: macOS-only -- shipyard's staging and packaging scripts run pkgbuild, productbuild and PlistBuddy
#   usage: package.sh     writes dist/longboat-<VER>.pkg and dist/build-info.txt; VER is the first line of ./VERSION
# spec: .superpowers/specs/2026-10-12-longboat-design.md "The Longboat product" -- one pkg, 10.9.5 floor,
#       the CLI in bin/, its scripts and the copied shipyard scripts in libexec/, and the updater
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
: "${SHIPYARD_SCRIPTS:=/usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts}"
BUILD_ROOT="${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}"

[ -f VERSION ] || { echo "package: no VERSION file; CI's ver job writes it" >&2; exit 1; }
VER="$(sed -n '1p' VERSION | tr -d ' \t')"
[ -n "$VER" ] || { echo "package: VERSION is empty" >&2; exit 1; }

pathfile="$BUILD_ROOT/longboat-updater.path"
[ -f "$pathfile" ] || { echo "package: no $pathfile; run build/build-updater.sh first" >&2; exit 1; }
APP="$(cat "$pathfile")"
[ -d "$APP" ] || { echo "package: the updater $APP is gone; rerun build/build-updater.sh" >&2; exit 1; }

SHIPYARD="$(cat components/shipyard/version)"
mkdir -p "$BUILD_ROOT"
WORK="$(mktemp -d "$BUILD_ROOT/longboat-package.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
trap 'exit 1' HUP INT TERM

P="$WORK/root/usr/local/mavergreen/longboat"
LIBEXEC="$P/libexec/longboat"
mkdir -p "$P/bin" "$LIBEXEC"
cp src/longboat "$P/bin/longboat"
chmod 755 "$P/bin/longboat"
for f in sdks.sh fetch.sh install.sh; do
  cp "src/$f" "$LIBEXEC/$f"
done
printf '%s\n' "$VER" > "$LIBEXEC/VERSION"
printf '%s\n' "${SHIPYARD#v}" > "$LIBEXEC/SHIPYARD_VERSION"
sh build/shipyard-scripts.sh extract "$SHIPYARD" "$LIBEXEC/shipyard"

sh "$SHIPYARD_SCRIPTS/stage_product.sh" --stage "$WORK/root" --product longboat \
  --name "Longboat for Mavericks" --version "$VER" --updater-app "$APP" \
  --scripts-out "$WORK/scripts" >&2
sh "$SHIPYARD_SCRIPTS/build_component_pkg.sh" --root "$WORK/root" --identifier dev.mavergreen.longboat \
  --version "$VER" --install-location / --scripts "$WORK/scripts" --out "$WORK/component.pkg" >&2

mkdir -p dist
rm -f "dist/longboat-$VER.pkg"
sh "$SHIPYARD_SCRIPTS/set_install_floor.sh" --identifier dev.mavergreen.longboat \
  --title "Longboat for Mavericks $VER" --component "$WORK/component.pkg" \
  --out "dist/longboat-$VER.pkg" --min-os 10.9.5 --base-version "${SHIPYARD#v}" --require-scripts >&2
sh "$SHIPYARD_SCRIPTS/build-info.sh" dist/build-info.txt "pkg=longboat-$VER.pkg" \
  identifier=dev.mavergreen.longboat "shipyard=$SHIPYARD"
echo "dist/longboat-$VER.pkg"
