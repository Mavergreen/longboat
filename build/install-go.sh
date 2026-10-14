#!/bin/sh
# platform: macOS-only -- shipyard's fetch_product_pkg.sh expands the toolchain pkg with pkgutil
#   usage: install-go.sh <version>
#          Puts the go126-cross toolchain of Mavergreen release <version> (components/golang/version)
#          at /usr/local/mavergreen/go126-cross, the path its pkg installs to, through shipyard's
#          fetch_product_pkg.sh, which verifies the pkg against the release's SHA256SUMS. Prints the
#          toolchain's bin directory, and nothing else, on stdout. Needs $SHIPYARD_SCRIPTS and sudo.
#          INSTALL_GO_ROOT replaces /usr/local/mavergreen (the tests put the toolchain in a temp dir).
#          install-go.sh --select <SHA256SUMS> <version>
#          Prints the SHA256SUMS line of <version>'s cross pkg, named golang-... or go126-...;
#          exits 1 when there is none.
set -eu
SHORT=go126-cross
ROOT_DIR="${INSTALL_GO_ROOT:-/usr/local/mavergreen}"

usage() {
  echo "usage: install-go.sh <version> | install-go.sh --select <SHA256SUMS> <version>" >&2
  exit 2
}

check_version() {
  case "$1" in
    *-mavericks.*) ;;
    *) echo "install-go: '$1' is not a <go version>-mavericks.<n> release" >&2; exit 2 ;;
  esac
}

# spec: mavergreen-conventions SKILL.md "Download the toolchain asset prefix-tolerantly" -- golang's
#       cross pkg was renamed go126- to golang- at 1.26.5-mavericks.1
select_pkg() {
  gover="${2%%-mavericks.*}"
  rev="${2#"$gover"-}"
  for pref in golang go126; do
    line="$(awk -v f="$pref-$gover-cross-$rev.pkg" '$2 == f' "$1")"
    if [ -n "$line" ]; then
      printf '%s\n' "$line"
      return 0
    fi
  done
  echo "install-go: $1 lists no cross pkg for $2 (neither golang-$gover-cross-$rev.pkg nor go126-$gover-cross-$rev.pkg)" >&2
  return 1
}

if [ "${1:-}" = --select ]; then
  [ "$#" -eq 3 ] || usage
  check_version "$3"
  [ -r "$2" ] || { echo "install-go: cannot read $2" >&2; exit 2; }
  select_pkg "$2" "$3"
  exit
fi
[ "$#" -eq 1 ] || usage
ver="$1"
check_version "$ver"
: "${SHIPYARD_SCRIPTS:?install-go: SHIPYARD_SCRIPTS must name the shipyard scripts directory (install@v1 sets it)}"

repo="$(sh "$SHIPYARD_SCRIPTS/product-name.sh" repo "$SHORT")"
d="$(mktemp -d "${TMPDIR:-/tmp}/install-go.XXXXXX")"
trap 'sudo rm -rf "$d/cache"; rm -rf "$d"' EXIT
curl -fsSL --retry 3 -o "$d/SHA256SUMS" "https://github.com/Mavergreen/$repo/releases/download/$ver/SHA256SUMS"
line="$(select_pkg "$d/SHA256SUMS" "$ver")"
pkg="${line##* }"
# platform: the pkg's go.env names CC by its absolute path under /usr/local/mavergreen/go126-cross,
#           so the toolchain goes where the pkg installs it; that directory's parent is root's
sudo env MAVERGREEN_PKG_CACHE="$d/cache" \
  sh "$SHIPYARD_SCRIPTS/fetch_product_pkg.sh" --asset "$pkg" "$SHORT" "$ver" "$ROOT_DIR/$SHORT" >&2
GOTOOLCHAIN=local "$ROOT_DIR/$SHORT/bin/go" version >&2
printf '%s\n' "$ROOT_DIR/$SHORT/bin"
