#!/bin/sh
# platform: macOS-only -- reads a built Cocoa app's Mach-O slices and Info.plist
set -eu
BUILD_ROOT="${MAVERICKS_BUILD_ROOT:-${TMPDIR:-/tmp}/mm-build}"
pathfile="$BUILD_ROOT/longboat-updater.path"
[ -f "$pathfile" ] || { echo "SKIP: build/build-updater.sh has not run"; exit 77; }
app="$(cat "$pathfile")"
[ -d "$app" ] || { echo "SKIP: $app is gone; rerun build/build-updater.sh"; exit 77; }
exe="$app/Contents/MacOS/longboat-updater"
plist="$app/Contents/Info.plist"
fail=0
check() { # name want got
  [ "$2" = "$3" ] && echo "ok: $1" || { echo "FAIL: $1: want '$2' got '$3'"; fail=1; }
}
archs=" $(lipo -info "$exe" | sed -n 's/.*: //p') "
case "$archs" in *" x86_64 "*) check "executable carries x86_64" yes yes ;; *) check "executable carries x86_64" yes "no ($archs)" ;; esac
case "$archs" in *" arm64 "*) check "executable carries arm64" yes yes ;; *) check "executable carries arm64" yes "no ($archs)" ;; esac
pb() { /usr/libexec/PlistBuddy -c "Print :$1" "$plist"; }
check "CFBundleIdentifier" dev.mavergreen.longboat.updater "$(pb CFBundleIdentifier)"
check "SUFeedURL" https://github.com/Mavergreen/longboat/releases/latest/download/longboat.xml "$(pb SUFeedURL)"
exit "$fail"
