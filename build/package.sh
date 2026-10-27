#!/bin/sh
# platform: macOS-only -- shipyard's staging and packaging scripts run stevedore, fetched for macOS
#   usage: package.sh     writes dist/longboat-<VER>.pkg and dist/build-info.txt; VER is the first line of ./VERSION.
#          It stages the pkg's tree in $BUILD_ROOT/longboat-stage/root and keeps it there afterwards. It
#          needs build/build-updater.sh's updater and build/build-installmedia.sh's darwin installmedia.
# spec: shipyard SKILL.md "Install layout and identity" -- one pkg, 10.9.5 floor,
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

IM="$BUILD_ROOT/installmedia/darwin-x86_64/installmedia"
[ -f "$IM" ] || { echo "package: no $IM; run build/build-installmedia.sh darwin $IM first" >&2; exit 1; }

SHIPYARD="$(cat components/shipyard/version)"
mkdir -p "$BUILD_ROOT"
WORK="$(mktemp -d "$BUILD_ROOT/longboat-package.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
trap 'exit 1' HUP INT TERM

STAGE="$BUILD_ROOT/longboat-stage"
rm -rf "$STAGE"
mkdir -p "$STAGE/root"
P="$STAGE/root/usr/local/mavergreen/longboat"
LIBEXEC="$P/libexec/longboat"
mkdir -p "$P/bin" "$LIBEXEC"
cp src/longboat "$P/bin/longboat"
chmod 755 "$P/bin/longboat"
for f in sdks.sh progress.sh fetch.sh install.sh uninstall.sh; do
  cp "src/$f" "$LIBEXEC/$f"
done
# spec: docs/longboat.1 -- the manual page goes where the mavergreen helper links share/man from
mkdir -p "$P/share/man/man1"
cp docs/longboat.1 "$P/share/man/man1/longboat.1"
cp "$IM" "$LIBEXEC/installmedia"
chmod 755 "$LIBEXEC/installmedia"
printf '%s\n' "$VER" > "$LIBEXEC/VERSION"
# spec: src/install.sh lb_own_epoch -- sdk install stamps the SDK pkg with this, as an owner's Mac has no git
EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}"
case "$EPOCH" in ''|*[!0-9]*) echo "package: no SOURCE_DATE_EPOCH and no git commit time to stamp SDK pkgs with" >&2; exit 1 ;; esac
printf '%s\n' "$EPOCH" > "$LIBEXEC/SOURCE_DATE_EPOCH"
printf '%s\n' "${SHIPYARD#v}" > "$LIBEXEC/SHIPYARD_VERSION"
sh build/shipyard-scripts.sh extract "$SHIPYARD" "$LIBEXEC/shipyard"

sh "$SHIPYARD_SCRIPTS/stage_product.sh" --stage "$STAGE/root" --product longboat \
  --name "Longboat for Mavericks" --version "$VER" --updater-app "$APP" \
  --scripts-out "$WORK/scripts" >&2
sh "$SHIPYARD_SCRIPTS/build_component_pkg.sh" --root "$STAGE/root" --identifier dev.mavergreen.longboat \
  --version "$VER" --install-location / --scripts "$WORK/scripts" --out "$WORK/component.pkg" >&2

mkdir -p dist
rm -f "dist/longboat-$VER.pkg"
sh "$SHIPYARD_SCRIPTS/set_install_floor.sh" --identifier dev.mavergreen.longboat \
  --title "Longboat for Mavericks $VER" --component "$WORK/component.pkg" \
  --out "dist/longboat-$VER.pkg" --min-os 10.9.5 --require-scripts >&2
sh "$SHIPYARD_SCRIPTS/build-info.sh" dist/build-info.txt "pkg=longboat-$VER.pkg" \
  identifier=dev.mavergreen.longboat "shipyard=$SHIPYARD"
echo "dist/longboat-$VER.pkg"
