#!/bin/sh
# platform: host-agnostic
#   usage: . fetch.sh     (sourced; needs sdks.sh and LONGBOAT_SHIPYARD_DIR)
#          lb_fetch <version> --into DIR   prints DIR/<sdkdir>; exit 2 on usage errors and unknown SDKs

lb_offered_versions() {
  lb_offered | awk '{ printf "%s%s", sep, $1; sep = " " } END { print "" }'
}

lb_fetch() {
  [ $# -eq 3 ] && [ "$2" = --into ] && [ -n "$3" ] || {
    echo "usage: longboat fetch <version> --into DIR" >&2
    return 2
  }
  _lb_line="$(lb_lookup "$1")" || {
    echo "longboat: unknown SDK '$1'; offered: $(lb_offered_versions)" >&2
    return 2
  }
  set -- "$3" $_lb_line
  _lb_err="$(MAVERICKS_SDK_CACHE="$1" sh "$LONGBOAT_SHIPYARD_DIR/fetch_sdk.sh" --arch "$4" 2>&1 >/dev/null)" || {
    printf '%s\n' "$_lb_err" >&2
    return 1
  }
  printf '%s/%s\n' "$1" "$5"
}
