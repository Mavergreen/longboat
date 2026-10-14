#!/bin/sh
# platform: host-agnostic
#   usage: build-installmedia.sh <goos> <out>
#          Builds installmedia/cmd/installmedia for <goos> (darwin or linux) on amd64, as <out>, with the
#          go on PATH, which CI makes the family's pinned Go (components/golang/version). The version it
#          reports is the first line of ./VERSION, or "dev" without one. Prints <out>.
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

usage() {
  echo "usage: build-installmedia.sh darwin|linux <out>" >&2
  exit 2
}

[ "$#" -eq 2 ] && [ -n "$2" ] || usage
case "$1" in
  darwin|linux) goos="$1" ;;
  *) usage ;;
esac
out="$2"
case "$out" in
  /*) ;;
  *) out="$(pwd)/$out" ;;
esac

VER=dev
if [ -f "$ROOT/VERSION" ]; then
  VER="$(sed -n '1p' "$ROOT/VERSION" | tr -d ' \t')"
  [ -n "$VER" ] || { echo "build-installmedia: VERSION is empty" >&2; exit 1; }
fi

mkdir -p "$(dirname "$out")"
cd "$ROOT/installmedia"
CGO_ENABLED=0 GOOS="$goos" GOARCH=amd64 GOTOOLCHAIN=local \
  go build -trimpath -buildvcs=false -ldflags "-s -w -buildid= -X main.version=$VER" -o "$out" ./cmd/installmedia
printf '%s\n' "$out"
