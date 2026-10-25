#!/bin/sh
# platform: macOS-only -- ditto stages the SDK, and the copied shipyard scripts run pkgbuild, productbuild and installer
#   usage: . install.sh     (sourced; needs sdks.sh, progress.sh, fetch.sh and LONGBOAT_SHIPYARD_DIR)
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
      --pkg-only) [ $# -ge 2 ] && [ -n "$2" ] || { echo "usage: longboat sdk install <version> [--pkg-only DIR]" >&2; return 2; }
        _lb_pkgonly="$2"; shift 2 ;;
      -*) echo "usage: longboat sdk install <version> [--pkg-only DIR]" >&2; return 2 ;;
      *) [ -z "$_lb_ver" ] || { echo "usage: longboat sdk install <version> [--pkg-only DIR]" >&2; return 2; }
        _lb_ver="$1"; shift ;;
    esac
  done
  [ -n "$_lb_ver" ] || { echo "usage: longboat sdk install <version> [--pkg-only DIR]" >&2; return 2; }
  _lb_line="$(lb_lookup "$_lb_ver")" || {
    [ $? -eq 1 ] || return 1
    echo "longboat: no SDK for version '$_lb_ver'; offered: $(lb_offered_versions)" >&2
    return 2
  }
  set -- $_lb_line
  _lb_short="$2" _lb_arch="$3" _lb_sdkdir="$4"

  if [ -z "$_lb_pkgonly" ] && [ "$(id -u)" -ne 0 ]; then
    echo "longboat: installing an SDK needs root: sudo longboat sdk install $_lb_ver" >&2
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
  if [ -n "$_lb_pkgonly" ]; then lb_progress_init 4; else lb_progress_init 5; fi
  _lb_work="$(mktemp -d "${TMPDIR:-/tmp}/longboat-install.XXXXXX")" || return 1
  trap 'rm -rf "$_lb_work"' EXIT
  trap 'exit 1' HUP INT TERM
  _lb_rc=0
  lb_install_build "$_lb_work" "$_lb_cache" "$_lb_ver" "$_lb_short" "$_lb_sdkdir" "$_lb_id" "$_lb_lbver" "$_lb_base" "$_lb_pkgonly" "$_lb_arch" || _lb_rc=$?
  trap - EXIT HUP INT TERM
  rm -rf "$_lb_work"
  return "$_lb_rc"
}

# spec: tests/install-test.sh -- test_sdk_name: the name Apple gave the OS at that version, "macOS" from 10.12
lb_sdk_name() {
  case "$1" in
    10.[0-9]|10.1[01]) printf 'Mac OS X %s\n' "$1" ;;
    *) printf 'macOS %s\n' "$1" ;;
  esac
}

# spec: tests/install-test.sh test_pkg_carries_sdk_settings_json_from_a_pristine_sdk -- lb_install_copy <sdk>
#       <dest> <sdkdir> <arch> copies the pristine SDK, then writes shipyard's SDKSettings.json for <arch> into
#       the copy (swiftc reads the SDK version only from it), unless the SDK ships its own
lb_install_copy() {
  mkdir -p "$2" || return 1
  ditto --norsrc --noextattr --noacl "$1" "$2/$3" || return 1
  _lb_jrc=0
  _lb_json="$(lb_sdk_settings_json "$4")" || _lb_jrc=$?
  case "$_lb_jrc" in
    0) printf '%s\n' "$_lb_json" > "$2/$3/SDKSettings.json" && chmod 644 "$2/$3/SDKSettings.json" ;;
    1) return 0 ;;
    *) echo "longboat: the shipyard scripts in $LONGBOAT_SHIPYARD_DIR cannot say what SDKSettings.json $3 needs" >&2
       return 1 ;;
  esac
}

lb_install_pkg() {
  sh "$_sh/stage_product.sh" --stage "$_w/root" --product "$_short" --name "$(lb_sdk_name "$_ver") SDK" \
    --version "$_lbver" --scripts-out "$_w/scripts" || return 1
  sh "$_sh/build_component_pkg.sh" --root "$_w/root" --identifier "$_id" --version "$_lbver" \
    --install-location / --scripts "$_w/scripts" --out "$_w/component.pkg"
}

lb_install_floor() {
  sh "$_sh/set_install_floor.sh" --identifier "$_id" --title "$(lb_sdk_name "$_ver") SDK (installed by Longboat)" \
    --component "$_w/component.pkg" --out "$_w/$_short-$_lbver.pkg" --min-os 10.9.5 \
    --base-version "$_base" --require-scripts
}

lb_install_build() {
  _w="$1" _cache="$2" _ver="$3" _short="$4" _sdkdir="$5" _id="$6" _lbver="$7" _base="$8" _out="$9" _arch="${10}"
  _sh="$LONGBOAT_SHIPYARD_DIR"
  _src="$(lb_fetch_step 1 "$_ver" --into "$_cache")" || return 1
  _dest="$_w/root/usr/local/mavergreen/$_short/SDKs"
  lb_step_run 2 "copying the SDK" lb_install_copy "$_src" "$_dest" "$_sdkdir" "$_arch" || return 1
  lb_step_run 3 "building the SDK pkg" lb_install_pkg || return 1
  lb_step_run 4 "adding the install floor" lb_install_floor || return 1
  if [ -n "$_out" ]; then
    mkdir -p "$_out" || return 1
    mv "$_w/$_short-$_lbver.pkg" "$_out/" || return 1
    printf '%s/%s-%s.pkg\n' "$_out" "$_short" "$_lbver"
    return 0
  fi
  lb_step_start 5 installing
  { installer -verboseR -pkg "$_w/$_short-$_lbver.pkg" -target /; echo $? > "$_w/installer.rc"; } 2>&1 \
    | lb_installer_bar 5 installing
  read -r _irc 2>/dev/null < "$_w/installer.rc" || _irc=1
  case "$_irc" in 0) ;; *) _irc=1 ;; esac
  lb_step_end 5 installing "$_irc"
  [ "$_irc" -eq 0 ] || return 1
  printf '/usr/local/mavergreen/%s/SDKs/%s\n' "$_short" "$_sdkdir"
}
