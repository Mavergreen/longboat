#!/bin/sh
# platform: host-agnostic
#   usage: . uninstall.sh     (sourced; needs sdks.sh and fetch.sh)
#          lb_uninstall <version>   runs the installed mavergreen helper's uninstall for <version>'s SDK;
#                                   exit 2 on a usage error, an unknown version or no root, 1 when that
#                                   SDK is not installed or an installed product links into it, else the
#                                   helper's status
#          lb_sdk_dependents <short>  each installed product, but <short>, whose tree has a link resolving
#                                   into /usr/local/mavergreen/<short>, one per line
# spec: tests/sdk-uninstall-test.sh -- the helper is the installed one, /usr/local/bin/mavergreen, which
#       manages every family product's receipts on this machine; LONGBOAT_ROOT moves it, for tests

# spec: tests/sdk-uninstall-test.sh test_uninstall_refuses_while_a_product_links_in,
#       test_uninstall_ignores_other_links -- an absolute link target is read under LONGBOAT_ROOT, as the
#       helper's --root reads one
lb_sdk_dependents() {
  _lb_mg="${LONGBOAT_ROOT:-}/usr/local/mavergreen"
  _lb_sdk="$(cd "$_lb_mg/$1" && pwd -P)" || return 1
  for _lb_p in "$_lb_mg"/*/; do
    _lb_p="${_lb_p%/}"
    [ "${_lb_p##*/}" != "$1" ] && [ -f "$_lb_p/mavergreen.plist" ] || continue
    find "$_lb_p" -type l 2>/dev/null | while IFS= read -r _lb_l; do
      _lb_t="$(readlink "$_lb_l")"
      case "$_lb_t" in
        /*) _lb_t="${LONGBOAT_ROOT:-}$_lb_t" ;;
        *) _lb_t="$(dirname "$_lb_l")/$_lb_t" ;;
      esac
      if [ -d "$_lb_t" ]; then
        _lb_r="$(cd "$_lb_t" && pwd -P)" || continue
      elif [ -e "$_lb_t" ]; then
        _lb_r="$(cd "$(dirname "$_lb_t")" && pwd -P)/${_lb_t##*/}" || continue
      else
        continue
      fi
      case "$_lb_r/" in
        "$_lb_sdk"/*) echo "${_lb_p##*/}"; break ;;
      esac
    done
  done
}

lb_uninstall() {
  [ $# -eq 1 ] && [ -n "$1" ] || { echo "usage: longboat sdk uninstall <version>" >&2; return 2; }
  _lb_line="$(lb_lookup "$1")" || {
    [ $? -eq 1 ] || return 1
    echo "longboat: no SDK for version '$1'; offered: $(lb_offered_versions)" >&2
    return 2
  }
  set -- "$1" $_lb_line
  [ "$(id -u)" -eq 0 ] || {
    echo "longboat: removing an SDK needs root: sudo longboat sdk uninstall $1" >&2
    return 2
  }
  [ -f "${LONGBOAT_ROOT:-}/usr/local/mavergreen/$3/mavergreen.plist" ] || {
    echo "longboat: the $1 SDK is not installed" >&2
    return 1
  }
  _lb_deps="$(lb_sdk_dependents "$3")" || return 1
  if [ -n "$_lb_deps" ]; then
    {
      echo "longboat: the $1 SDK is in use: these products link into $3, so remove them first:"
      printf '%s\n' "$_lb_deps" | sed 's/^/  sudo mavergreen uninstall /'
    } >&2
    return 1
  fi
  _lb_helper="${LONGBOAT_ROOT:-}/usr/local/bin/mavergreen"
  [ -x "$_lb_helper" ] || {
    echo "longboat: no mavergreen helper at $_lb_helper; reinstall Longboat's pkg, which carries it" >&2
    return 1
  }
  exec "$_lb_helper" uninstall "$3"
}
