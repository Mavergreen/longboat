#!/bin/sh
# platform: host-agnostic
#   usage: . sdks.sh     (sourced; needs LONGBOAT_SHIPYARD_DIR)
#          lb_offered           one line per offered SDK: <version> <short> <arch> <sdkdir>
#          lb_lookup <version>  the one matching line, or return 1
# spec: .superpowers/specs/2026-10-12-longboat-design.md "SDK identity" -- an SDK is offered when the
#       copied sdk-pins.sh can fetch it and the copied registry has a short name for it.

lb_offered() {
  (
    . "$LONGBOAT_SHIPYARD_DIR/sdk-pins.sh"
    seen=" "
    for arch in x86_64 arm64 i386 ppc; do
      pin="$(mav_sdk_pin "$arch")" || continue
      set -- $pin
      dir="$4"
      ver="${dir#MacOSX}"
      ver="${ver%.sdk}"
      ver="${ver%%[A-Za-z]*}"
      [ -n "$ver" ] || continue
      short="sdk$(printf '%s' "$ver" | tr -d .)"
      sh "$LONGBOAT_SHIPYARD_DIR/product-name.sh" identifier "$short" >/dev/null 2>&1 || continue
      case "$seen" in *" $ver "*) continue ;; esac
      seen="$seen$ver "
      echo "$ver $short $arch $dir"
    done
  )
}

lb_lookup() {
  [ -n "$1" ] || return 1
  _lb_hit="$(lb_offered | awk -v v="$1" '$1 == v { print; exit }')"
  [ -n "$_lb_hit" ] || return 1
  printf '%s\n' "$_lb_hit"
}
