#!/bin/sh
# platform: host-agnostic -- expands the shipyard release pkg with pkgutil on macOS, bsdtar elsewhere
# spec: INGREDIENTS.md "The shipyard scripts digest"
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
LIST="$root/build/shipyard-scripts.list"
SPATH=usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts

usage() {
  echo "usage: shipyard-scripts.sh extract <vX.Y.Z> <OUTDIR> | digest <DIR> | check" >&2
  exit 2
}

# platform: 10.9 and every Mac have shasum; a stock Linux has only coreutils' sha256sum
sha() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 < "$1" | cut -c1-64
  else sha256sum < "$1" | cut -c1-64
  fi
}

listed() { grep . "$LIST"; }

# platform: expand_pkg <PKG> <DIR> unpacks the pkg's xar into DIR with its components' Payloads intact;
#           10.9's bsdtar (libarchive 2.8.3) mangles the xar's directory names, so a Mac keeps pkgutil
expand_pkg() {
  case "$(uname -s)" in
    Darwin)
      # platform: guarded macOS-only call -- uname -s is Darwin
      pkgutil --expand "$1" "$2" ;;
    *) mkdir "$2" && (cd "$2" && bsdtar -xf "$1") ;;
  esac
}

extract() {
  ver=$1 out=$2
  T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-shipyard.XXXXXX")
  trap 'rm -rf "$T"' EXIT
  pkg=${LONGBOAT_SHIPYARD_PKG:-}
  sums=${LONGBOAT_SHIPYARD_SUMS:-}
  if [ -z "$pkg" ] || [ -z "$sums" ]; then
    mkdir "$T/dl"
    gh release download "$ver" -R Mavergreen/shipyard \
      -p "mavericks-shipyard-${ver#v}.pkg" -p SHA256SUMS -D "$T/dl" >&2 \
      || { echo "shipyard-scripts: cannot download shipyard $ver" >&2; exit 1; }
    pkg=$T/dl/mavericks-shipyard-${ver#v}.pkg
    sums=$T/dl/SHA256SUMS
  fi
  name=$(basename "$pkg")
  want=$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "$sums")
  [ -n "$want" ] || { echo "shipyard-scripts: $name is not listed in $sums" >&2; exit 1; }
  got=$(sha "$pkg")
  [ "$got" = "$want" ] || { echo "shipyard-scripts: $name sha256 $got does not match $want" >&2; exit 1; }

  case "$pkg" in /*) ;; *) pkg=$PWD/$pkg ;; esac
  expand_pkg "$pkg" "$T/x" >/dev/null 2>&1 \
    || { echo "shipyard-scripts: cannot expand $name" >&2; exit 1; }
  mkdir "$T/p"
  find "$T/x" -type f -name Payload > "$T/payloads"
  hits=0
  while IFS= read -r payload; do
    gzip -dc "$payload" > "$T/raw" 2>/dev/null \
      || { echo "shipyard-scripts: cannot decompress $payload" >&2; exit 1; }
    cpio -it < "$T/raw" > "$T/names" 2>/dev/null \
      || { echo "shipyard-scripts: cannot list $payload" >&2; exit 1; }
    grep -q "$SPATH/" "$T/names" || continue
    hits=$((hits + 1))
    (cd "$T/p" && cpio -id --quiet < "$T/raw") >/dev/null 2>&1 \
      || { echo "shipyard-scripts: cannot extract $payload" >&2; exit 1; }
  done < "$T/payloads"
  [ "$hits" -eq 1 ] || { echo "shipyard-scripts: $hits components of $name hold $SPATH, want exactly 1" >&2; exit 1; }
  src=$T/p/$SPATH
  [ -d "$src" ] || { echo "shipyard-scripts: no $SPATH in $name" >&2; exit 1; }

  missing=0
  while IFS= read -r p; do
    [ -f "$src/$p" ] || { echo "shipyard-scripts: $name lacks $p" >&2; missing=1; }
  done <<EOL
$(listed)
EOL
  [ "$missing" -eq 0 ] || exit 1

  while IFS= read -r p; do
    mkdir -p "$out/$(dirname "$p")"
    cp "$src/$p" "$out/$p"
  done <<EOL
$(listed)
EOL
}

digest() {
  dir=$1
  while IFS= read -r p; do
    [ -f "$dir/$p" ] || { echo "shipyard-scripts: $dir lacks $p" >&2; exit 1; }
    printf '%s  %s\n' "$(sha "$dir/$p")" "$p"
  done <<EOL
$(listed)
EOL
}

check() {
  committed=${LONGBOAT_SCRIPTS_SHA256:-$root/components/shipyard/scripts.sha256}
  ver=$(cat "$root/components/shipyard/version")
  C=$(mktemp -d "${TMPDIR:-/tmp}/longboat-check.XXXXXX")
  trap 'rm -rf "$C"' EXIT
  sh "$0" extract "$ver" "$C/set" || exit 1
  sh "$0" digest "$C/set" > "$C/digest" || exit 1
  [ -f "$committed" ] || { echo "shipyard-scripts: no committed digest $committed" >&2; exit 1; }
  rc=0
  diff "$committed" "$C/digest" >&2 || rc=$?
  [ "$rc" -ne 0 ] || exit 0
  [ "$rc" -eq 1 ] || { echo "shipyard-scripts: diff failed" >&2; exit 1; }
  echo "shipyard-scripts: $committed is stale for shipyard $ver" >&2
  exit 3
}

[ "$#" -ge 1 ] || usage
cmd=$1; shift
case "$cmd" in
  extract) [ "$#" -eq 2 ] || usage; extract "$1" "$2" ;;
  digest) [ "$#" -eq 1 ] || usage; digest "$1" ;;
  check) [ "$#" -eq 0 ] || usage; check ;;
  *) usage ;;
esac
