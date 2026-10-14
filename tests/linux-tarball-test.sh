#!/bin/sh
# platform: host-agnostic
# spec: build/package-linux.sh -- the same stage packs to the same bytes, whatever its umask, mtimes and
#       creation order, under opt/mavergreen/longboat/ only, with the linux installmedia in place of the
#       Mach-O one and without the updater or the manifest
set -eu
command -v go >/dev/null 2>&1 || { echo "linux-tarball-test: no go on PATH -- skipping"; exit 77; }
root=$(cd "$(dirname "$0")/.." && pwd)

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-linux-tarball.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

VER=20261014.7
TGZ="dist/longboat-$VER-linux-x86_64.tar.gz"
P=opt/mavergreen/longboat

# spec: build/package-linux.sh finds its repo from its own path, so a copy of it and of reprotar in $T
#       packs into $T/repo/dist and never into this tree's dist/
mkdir -p "$T/repo/build"
cp "$root/build/package-linux.sh" "$T/repo/build/package-linux.sh"
cp -R "$root/build/reprotar" "$T/repo/build/reprotar"
printf '%s\n' "$VER" > "$T/repo/VERSION"

# spec: build/package.sh's stage -- $1 is the build root, $2 the umask, $3 a touch -t stamp for every
#       file, and $4 "reverse" to create the files in the opposite order
stage() {
  (
    umask "$2"
    B=$1
    R="$B/longboat-stage/root"
    L="$R/usr/local/mavergreen/longboat/libexec/longboat"
    list="bin/longboat=$root/src/longboat"
    for f in sdks.sh progress.sh fetch.sh install.sh; do
      list="$list
libexec/longboat/$f=$root/src/$f"
    done
    printf '%s\n' "$list" > "$B.list"
    if [ "$4" = reverse ]; then
      sed -n '1!G;h;$p' "$B.list" > "$B.rev"
      mv "$B.rev" "$B.list"
    fi
    mkdir -p "$R/usr/local/mavergreen/longboat/bin" "$L/shipyard/templates"
    while IFS= read -r pair; do
      cp "${pair#*=}" "$R/usr/local/mavergreen/longboat/${pair%%=*}"
    done < "$B.list"
    chmod 755 "$R/usr/local/mavergreen/longboat/bin/longboat"
    printf 'Mach-O stand-in\n' > "$L/installmedia"
    chmod 755 "$L/installmedia"
    printf '%s\n' "$VER" > "$L/VERSION"
    printf '1.0.466\n' > "$L/SHIPYARD_VERSION"
    printf 'longboat Mavergreen/longboat\n' > "$L/shipyard/product-names"
    printf '#!/bin/sh\n' > "$L/shipyard/templates/base-postinstall.sh"
    printf '<plist/>\n' > "$R/usr/local/mavergreen/longboat/mavergreen.plist"
    mkdir -p "$R/Library/Application Support/Mavergreen/longboat-updater.app/Contents"
    printf '<plist/>\n' > "$R/Library/Application Support/Mavergreen/longboat-updater.app/Contents/Info.plist"
    mkdir -p "$B/installmedia/linux-x86_64"
    printf '#!/bin/sh\necho "linux installmedia $*"\nexit 7\n' > "$B/installmedia/linux-x86_64/installmedia"
    find "$B" -exec touch -t "$3" {} +
  )
}

# spec: build/package-linux.sh -- its environment is the build root and SOURCE_DATE_EPOCH; it prints
#       the tarball's path relative to the repo
package() {
  rc=0
  (cd "$T" && MAVERICKS_BUILD_ROOT="$1" sh "$T/repo/build/package-linux.sh") >"$T/out" 2>"$T/err" || rc=$?
}

stage "$T/a" 022 200001010000 forward
stage "$T/b" 077 202601020304 reverse
SOURCE_DATE_EPOCH=1700000000
export SOURCE_DATE_EPOCH
package "$T/a"
[ "$rc" -eq 0 ] || { echo "FAIL: package-linux.sh exited $rc: $(cat "$T/err")" >&2; exit 1; }
cp "$T/repo/$TGZ" "$T/a.tar.gz"
cp "$T/out" "$T/a.out"
package "$T/b"
[ "$rc" -eq 0 ] || { echo "FAIL: package-linux.sh exited $rc: $(cat "$T/err")" >&2; exit 1; }
cp "$T/repo/$TGZ" "$T/b.tar.gz"

test_prints_the_tarball() {
  [ "$(cat "$T/a.out")" = "$TGZ" ] || { echo "printed $(cat "$T/a.out"), want $TGZ" >&2; return 1; }
}

test_same_stage_same_bytes() {
  cmp "$T/a.tar.gz" "$T/b.tar.gz" >&2
}

test_everything_is_under_the_prefix() {
  tar -tzf "$T/a.tar.gz" > "$T/list" || return 1
  [ -s "$T/list" ] || { echo "the tarball lists nothing" >&2; return 1; }
  if grep -v "^$P/" "$T/list" >&2; then echo "^ outside $P/" >&2; return 1; fi
  for want in "$P/bin/longboat" "$P/libexec/longboat/installmedia" "$P/libexec/longboat/fetch.sh" \
              "$P/libexec/longboat/VERSION" "$P/libexec/longboat/shipyard/product-names"; do
    grep -qx "$want" "$T/list" || { echo "no $want in the tarball" >&2; return 1; }
  done
}

test_no_updater_and_no_manifest() {
  tar -tzf "$T/a.tar.gz" > "$T/list" || return 1
  if grep -e 'mavergreen\.plist' -e 'updater' -e 'Library' "$T/list" >&2; then
    echo "^ the Linux tarball carries the macOS updater or manifest" >&2; return 1
  fi
}

test_the_extracted_longboat_runs_the_linux_installmedia() {
  mkdir "$T/x"
  tar -xzf "$T/a.tar.gz" -C "$T/x" || return 1
  lb="$T/x/$P/bin/longboat"
  [ -x "$lb" ] && [ -x "$T/x/$P/libexec/longboat/installmedia" ] \
    || { echo "longboat or installmedia is not executable once extracted" >&2; return 1; }
  "$lb" --help > "$T/help" 2>&1 && grep -q 'verify installer <release> <image>' "$T/help" \
    || { echo "longboat --help: $(cat "$T/help")" >&2; return 1; }
  rc=0
  "$lb" verify installer 10.6 /dev/null > "$T/vout" 2>&1 || rc=$?
  [ "$rc" -eq 7 ] && grep -qx 'linux installmedia verify installer 10.6 /dev/null' "$T/vout" \
    || { echo "longboat verify installer exited $rc: $(cat "$T/vout")" >&2; return 1; }
}

# spec: build/package-linux.sh -- it refuses, writing nothing, without SOURCE_DATE_EPOCH, the stage, the linux
#       installmedia or a VERSION
needs() {
  rm -rf "$T/repo/dist"
  package "$1"
  [ "$rc" -ne 0 ] && [ ! -e "$T/repo/$TGZ" ] && grep -q "$2" "$T/err" \
    || { echo "package-linux.sh exited $rc, wrote $(ls "$T/repo/dist" 2>/dev/null), said: $(cat "$T/err")" >&2; return 1; }
}

test_refuses_without_source_date_epoch() {
  (unset SOURCE_DATE_EPOCH; needs "$T/a" SOURCE_DATE_EPOCH)
}

test_refuses_without_the_stage() {
  needs "$T/none" longboat-stage
}

test_refuses_without_the_linux_installmedia() {
  mv "$T/a/installmedia" "$T/a-installmedia"
  r=0; needs "$T/a" installmedia || r=1
  mv "$T/a-installmedia" "$T/a/installmedia"
  return "$r"
}

test_refuses_without_a_version() {
  mv "$T/repo/VERSION" "$T/VERSION.away"
  r=0; needs "$T/a" VERSION || r=1
  mv "$T/VERSION.away" "$T/repo/VERSION"
  return "$r"
}

check test_prints_the_tarball
check test_same_stage_same_bytes
check test_everything_is_under_the_prefix
check test_no_updater_and_no_manifest
check test_the_extracted_longboat_runs_the_linux_installmedia
check test_refuses_without_source_date_epoch
check test_refuses_without_the_stage
check test_refuses_without_the_linux_installmedia
check test_refuses_without_a_version
exit "$fail"
