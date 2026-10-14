#!/bin/sh
# platform: host-agnostic
#   usage: package-linux.sh   writes dist/longboat-<VER>-linux-x86_64.tar.gz, VER being the first line of
#          ./VERSION, and prints its path. It packs bin/longboat and libexec/longboat/ from build/package.sh's
#          stage in $BUILD_ROOT/longboat-stage/root, with build/build-installmedia.sh's linux installmedia in
#          place of the darwin one, and neither the updater nor the manifest. SOURCE_DATE_EPOCH must be set:
#          it is every entry's mtime. It builds build/reprotar with the go on PATH, for this host.
# spec: INGREDIENTS.md "Conformance deviations", linux -- a tarball under opt/mavergreen/longboat/, x86_64
#       only, reproducible: the same stage and SOURCE_DATE_EPOCH give the same bytes with the family's
#       pinned Go (components/golang/version), which CI puts on PATH. Another Go may not: reprotar's gzip
#       stream comes from compress/flate, whose output Go does not promise to keep across releases, and
#       the linux installmedia it packs is built by a Go too
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BUILD_ROOT="${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}"

[ -f VERSION ] || { echo "package-linux: no VERSION file; CI's ver job writes it" >&2; exit 1; }
VER="$(sed -n '1p' VERSION | tr -d ' \t')"
[ -n "$VER" ] || { echo "package-linux: VERSION is empty" >&2; exit 1; }
[ -n "${SOURCE_DATE_EPOCH:-}" ] \
  || { echo "package-linux: SOURCE_DATE_EPOCH is unset; CI sets it to HEAD's commit time (git log -1 --format=%ct)" >&2; exit 1; }

P="$BUILD_ROOT/longboat-stage/root/usr/local/mavergreen/longboat"
[ -f "$P/bin/longboat" ] && [ -d "$P/libexec/longboat" ] \
  || { echo "package-linux: no $BUILD_ROOT/longboat-stage; run build/package.sh first" >&2; exit 1; }
IM="$BUILD_ROOT/installmedia/linux-x86_64/installmedia"
[ -f "$IM" ] || { echo "package-linux: no $IM; run build/build-installmedia.sh linux $IM first" >&2; exit 1; }

mkdir -p "$BUILD_ROOT"
WORK="$(mktemp -d "$BUILD_ROOT/longboat-package-linux.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
trap 'exit 1' HUP INT TERM

# platform: the family's cross Go defaults GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 in its go.env, so
#           reprotar, which runs here and ships nowhere, is built for this host instead
(
  cd build/reprotar
  GOOS="$(GOTOOLCHAIN=local go env GOHOSTOS)" GOARCH="$(GOTOOLCHAIN=local go env GOHOSTARCH)" \
    CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -o "$WORK/reprotar" .
)

mkdir -p "$WORK/root/bin" "$WORK/root/libexec"
cp "$P/bin/longboat" "$WORK/root/bin/longboat"
cp -R "$P/libexec/longboat" "$WORK/root/libexec/longboat"
cp "$IM" "$WORK/root/libexec/longboat/installmedia"
chmod 755 "$WORK/root/bin/longboat" "$WORK/root/libexec/longboat/installmedia"

mkdir -p dist
out="dist/longboat-$VER-linux-x86_64.tar.gz"
rm -f "$out"
"$WORK/reprotar" -root "$WORK/root" -prefix opt/mavergreen/longboat -out "$out"
echo "$out"
