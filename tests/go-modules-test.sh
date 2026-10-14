#!/bin/sh
# platform: host-agnostic
set -eu
command -v go >/dev/null 2>&1 || { echo "go-modules-test: no go on PATH -- skipping"; exit 77; }
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# platform: the family's cross toolchain's go.env defaults to GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 with
#           its 10.9 cross CC, which would run every test binary under Rosetta on an arm64 Mac; these
#           tests are of the code, so they build for the host, and with cgo only for -race, which needs it
GOOS="$(GOTOOLCHAIN=local go env GOHOSTOS)"
GOARCH="$(GOTOOLCHAIN=local go env GOHOSTARCH)"
export GOOS GOARCH
race=""
if [ "${LONGBOAT_GO_RACE:-}" = 1 ]; then
  race=-race
else
  CGO_ENABLED=0
  export CGO_ENABLED
fi
mods="$(cd "$ROOT" && git ls-files '*go.mod')"
[ -n "$mods" ] || { echo "FAIL: git ls-files finds no go.mod, so this test would test nothing" >&2; exit 1; }
fail=0
IFS='
'
for m in $mods; do
  dir="$(dirname "$m")"
  if (cd "$ROOT/$dir" && GOTOOLCHAIN=local go vet ./... && GOTOOLCHAIN=local go test $race ./...); then
    echo "ok: $dir"
  else
    echo "FAIL: $dir: go vet or go test failed" >&2
    fail=1
  fi
done
exit "$fail"
