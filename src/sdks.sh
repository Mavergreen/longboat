#!/bin/sh
# platform: host-agnostic
#   usage: . sdks.sh     (sourced; needs LONGBOAT_SHIPYARD_DIR)
#          lb_offered           one line per offered SDK: <version> <short> <arch> <sdkdir>;
#                               non-zero when the copied shipyard scripts cannot answer
#          lb_lookup <version>  the one matching line; return 1 when none matches, 3 when lb_offered fails
# spec: tests/fetch-test.sh -- test_list_offers_10_9, test_list_hides_unregistered: an SDK is offered
#       when the copied sdk-pins.sh can fetch it and the copied registry has a short name for it.

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
      rc=0
      sh "$LONGBOAT_SHIPYARD_DIR/product-name.sh" identifier "$short" >/dev/null || rc=$?
      case "$rc" in
        0) ;;
        1) continue ;;
        *) echo "longboat: the shipyard scripts in $LONGBOAT_SHIPYARD_DIR cannot say whether $short is registered" >&2
           exit 1 ;;
      esac
      case "$seen" in *" $ver "*) continue ;; esac
      seen="$seen$ver "
      echo "$ver $short $arch $dir"
    done
  )
}

lb_lookup() {
  [ -n "$1" ] || return 1
  _lb_all="$(lb_offered)" || return 3
  _lb_hit="$(printf '%s\n' "$_lb_all" | awk -v v="$1" '$1 == v { print; exit }')"
  [ -n "$_lb_hit" ] || return 1
  printf '%s\n' "$_lb_hit"
}
