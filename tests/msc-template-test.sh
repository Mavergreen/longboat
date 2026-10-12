#!/bin/sh
# platform: host-agnostic
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cmp -s "$root/build/msc.sh" "$SHIPYARD_SCRIPTS/templates/msc.sh" || {
    echo "FAIL: build/msc.sh must equal shipyard's template byte for byte (check 17)" >&2
    exit 1
}
echo "ok: build/msc.sh matches shipyard's template"
