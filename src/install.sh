#!/bin/sh
# platform: macOS-only -- ditto stages the SDK, and the copied shipyard scripts run pkgbuild, productbuild and installer
#   usage: . install.sh     (sourced; needs sdks.sh, fetch.sh and LONGBOAT_SHIPYARD_DIR)
#          lb_install <version> [--pkg-only DIR]
# spec: tests/install-test.sh -- test_install_needs_root, test_home_untouched, test_pkg_only_builds_conformant_pkg:
#       install fetches, stages the SDK as its own product tree, builds the pkg with shipyard's copied
#       scripts, and installs it as root; the cache is under /usr/local/mavergreen/var/longboat as root, never $HOME.

lb_selfdir() { cd "$(dirname "${LB_INSTALL_SH:-$0}")" && pwd; }

lb_own_version() {
  _lb_d="$(lb_selfdir)"
  if [ -f "$_lb_d/VERSION" ]; then
    sed -n '1p' "$_lb_d/VERSION" | tr -d ' \t'
  elif [ -n "${LONGBOAT_VERSION:-}" ]; then
    printf '%s\n' "$LONGBOAT_VERSION"
  else
    return 1
  fi
}

lb_shipyard_version() {
  _lb_d="$(lb_selfdir)"
  for _lb_f in "$_lb_d/SHIPYARD_VERSION" "$_lb_d/../components/shipyard/version"; do
    if [ -f "$_lb_f" ]; then
      sed -n '1p' "$_lb_f" | tr -d ' \t' | sed 's/^v//'
      return 0
    fi
  done
  return 1
}

lb_install() {
  _lb_ver="" _lb_pkgonly=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --pkg-only) [ $# -ge 2 ] && [ -n "$2" ] || { echo "usage: longboat install <version> [--pkg-only DIR]" >&2; return 2; }
        _lb_pkgonly="$2"; shift 2 ;;
      -*) echo "usage: longboat install <version> [--pkg-only DIR]" >&2; return 2 ;;
      *) [ -z "$_lb_ver" ] || { echo "usage: longboat install <version> [--pkg-only DIR]" >&2; return 2; }
        _lb_ver="$1"; shift ;;
    esac
  done
  [ -n "$_lb_ver" ] || { echo "usage: longboat install <version> [--pkg-only DIR]" >&2; return 2; }
  _lb_line="$(lb_lookup "$_lb_ver")" || {
    [ $? -eq 1 ] || return 1
    echo "longboat: unknown SDK '$_lb_ver'; offered: $(lb_offered_versions)" >&2
    return 2
  }
  set -- $_lb_line
  _lb_short="$2" _lb_sdkdir="$4"

  if [ -z "$_lb_pkgonly" ] && [ "$(id -u)" -ne 0 ]; then
    echo "longboat: installing an SDK needs root: sudo longboat install $_lb_ver" >&2
    return 2
  fi
  _lb_lbver="$(lb_own_version)" || {
    echo "longboat: no VERSION file beside install.sh and LONGBOAT_VERSION is not set; cannot version the pkg" >&2
    return 2
  }
  _lb_base="$(lb_shipyard_version)" || {
    echo "longboat: cannot find the pinned shipyard version (SHIPYARD_VERSION)" >&2
    return 2
  }
  _lb_id="$(sh "$LONGBOAT_SHIPYARD_DIR/product-name.sh" identifier "$_lb_short")" || return 1

  if [ -n "$_lb_pkgonly" ]; then
    _lb_cache="$_lb_pkgonly/cache"
  else
    _lb_cache="${LONGBOAT_ROOT:-}/usr/local/mavergreen/var/longboat"
  fi
  _lb_work="$(mktemp -d "${TMPDIR:-/tmp}/longboat-install.XXXXXX")" || return 1
  trap 'rm -rf "$_lb_work"' EXIT
  trap 'exit 1' HUP INT TERM
  _lb_rc=0
  lb_install_build "$_lb_work" "$_lb_cache" "$_lb_ver" "$_lb_short" "$_lb_sdkdir" "$_lb_id" "$_lb_lbver" "$_lb_base" "$_lb_pkgonly" || _lb_rc=$?
  trap - EXIT HUP INT TERM
  rm -rf "$_lb_work"
  return "$_lb_rc"
}

lb_install_build() {
  _w="$1" _cache="$2" _ver="$3" _short="$4" _sdkdir="$5" _id="$6" _lbver="$7" _base="$8" _out="$9"
  _sh="$LONGBOAT_SHIPYARD_DIR"
  _src="$(lb_fetch "$_ver" --into "$_cache")" || return 1
  _dest="$_w/root/usr/local/mavergreen/$_short/SDKs"
  mkdir -p "$_dest" || return 1
  ditto --norsrc --noextattr --noacl "$_src" "$_dest/$_sdkdir" || return 1
  sh "$_sh/stage_product.sh" --stage "$_w/root" --product "$_short" --name "Mac OS X $_ver SDK" \
    --version "$_lbver" --scripts-out "$_w/scripts" >&2 || return 1
  sh "$_sh/build_component_pkg.sh" --root "$_w/root" --identifier "$_id" --version "$_lbver" \
    --install-location / --scripts "$_w/scripts" --out "$_w/component.pkg" >&2 || return 1
  sh "$_sh/set_install_floor.sh" --identifier "$_id" --title "Mac OS X $_ver SDK (installed by Longboat)" \
    --component "$_w/component.pkg" --out "$_w/$_short-$_lbver.pkg" --min-os 10.9.5 \
    --base-version "$_base" --require-scripts >&2 || return 1
  if [ -n "$_out" ]; then
    mkdir -p "$_out" || return 1
    mv "$_w/$_short-$_lbver.pkg" "$_out/" || return 1
    printf '%s/%s-%s.pkg\n' "$_out" "$_short" "$_lbver"
    return 0
  fi
  installer -pkg "$_w/$_short-$_lbver.pkg" -target / >&2 || return 1
  printf '/usr/local/mavergreen/%s/SDKs/%s\n' "$_short" "$_sdkdir"
}
