#!/bin/sh
# platform: host-agnostic
#   usage: . fetch.sh     (sourced; needs sdks.sh and LONGBOAT_SHIPYARD_DIR)
#          lb_fetch <version> --into DIR   prints DIR/<sdkdir>; exit 2 on usage errors and unknown SDKs,
#                                          1 when the copied shipyard scripts fail
#          lb_fetch_step <n> <version> --into DIR   lb_fetch as numbered step n (needs progress.sh): shown while it
#                                          works, or noted "cached" when DIR already holds the SDK

lb_offered_versions() {
  _lb_all="$(lb_offered)" || return 1
  printf '%s\n' "$_lb_all" | awk 'NF { printf "%s%s", sep, $1; sep = " " } END { print "" }'
}

lb_fetch() {
  [ $# -eq 3 ] && [ "$2" = --into ] && [ -n "$3" ] || {
    echo "usage: longboat sdk fetch <version> --into DIR" >&2
    return 2
  }
  _lb_line="$(lb_lookup "$1")" || {
    [ $? -eq 1 ] || return 1
    echo "longboat: no SDK for version '$1'; offered: $(lb_offered_versions)" >&2
    return 2
  }
  set -- "$3" $_lb_line
  # spec: tests/fetch-test.sh test_fetch_opts_in_and_prints_fetch_sdks_path -- with _lb_fetch_json set (sdk
  #       fetch), opt in to shipyard's SDKSettings.json and print the SDK fetch_sdk.sh prints, which may be a
  #       view of symlinks into the pristine one; otherwise (sdk install, which copies the SDK into a pkg) never
  #       opt in, whatever the caller's environment says
  if [ -n "${_lb_fetch_json:-}" ]; then
    _lb_sdk="$(MAVERICKS_SDK_SETTINGS_JSON=1 MAVERICKS_SDK_CACHE="$1" sh "$LONGBOAT_SHIPYARD_DIR/fetch_sdk.sh" --arch "$4")" \
      || return 1
  else
    _lb_sdk="$(unset MAVERICKS_SDK_SETTINGS_JSON
      MAVERICKS_SDK_CACHE="$1" sh "$LONGBOAT_SHIPYARD_DIR/fetch_sdk.sh" --arch "$4")" || return 1
  fi
  _lb_sdk="$(printf '%s\n' "$_lb_sdk" | sed -n '$p')"
  [ -d "$_lb_sdk" ] || { echo "longboat: fetch_sdk.sh printed '$_lb_sdk', which is not a directory" >&2; return 1; }
  printf '%s\n' "$_lb_sdk"
}

lb_fetch_step() {
  _lb_fs_n="$1"
  shift
  if [ $# -eq 3 ] && [ "$2" = --into ] && [ -n "$3" ] && _lb_fs_line="$(lb_lookup "$1")"; then
    set -- "$1" "$2" "$3" $_lb_fs_line
    if [ -d "$3/$7" ]; then
      lb_step_note "$_lb_fs_n" "fetching the $1 SDK" cached
      lb_fetch "$1" "$2" "$3"
      return
    fi
    lb_step_show "$_lb_fs_n" "fetching the $1 SDK" lb_fetch "$1" "$2" "$3"
    return
  fi
  lb_fetch "$@"
}
