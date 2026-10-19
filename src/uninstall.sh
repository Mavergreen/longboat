#!/bin/sh
# platform: host-agnostic
#   usage: . uninstall.sh     (sourced; needs sdks.sh and fetch.sh)
#          lb_uninstall <version>   runs the installed mavergreen helper's uninstall for <version>'s SDK;
#                                   exit 2 on a usage error, an unknown version or no root, 1 when that
#                                   SDK is not installed, else the helper's status
# spec: tests/sdk-uninstall-test.sh -- the helper is the installed one, /usr/local/bin/mavergreen, which
#       manages every family product's receipts on this machine; LONGBOAT_ROOT moves it, for tests

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
  _lb_helper="${LONGBOAT_ROOT:-}/usr/local/bin/mavergreen"
  [ -x "$_lb_helper" ] || {
    echo "longboat: no mavergreen helper at $_lb_helper; reinstall Longboat's pkg, which carries it" >&2
    return 1
  }
  exec "$_lb_helper" uninstall "$3"
}
