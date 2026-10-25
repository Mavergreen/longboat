#!/bin/sh
# platform: macOS-only -- runs in release.yml's vm-test job, inside the Mac OS X 10.9 guest: installer, pkgutil, sudo
#   usage: build/vm-acceptance.sh <PKG> <VERSION>
#          installs PKG, proves it is VERSION, lists both jobs, installs the real 10.9 SDK with it, then
#          uninstalls the SDK and Longboat and proves nothing of either is left. Exit 1 at the first wrong thing.
# spec: shipyard check 29 -- a pkg-shipping repo installs its run's own pkg on 10.9 and runs it before publish
set -eu
[ "$#" -eq 2 ] || { echo "usage: build/vm-acceptance.sh <PKG> <VERSION>" >&2; exit 2; }
pkg=$1 ver=$2
MG=/usr/local/mavergreen
lb=$MG/bin/longboat
sdk=$MG/sdk109/SDKs/MacOSX10.9.sdk
# platform: the family's base component installs the mavergreen helper at /usr/local/bin, outside the prefix
helper=/usr/local/bin/mavergreen

step() { printf '== %s\n' "$*"; }
fail() { echo "vm-acceptance: $*" >&2; exit 1; }

sw_vers
case "$(sw_vers -productVersion)" in 10.9|10.9.*) ;; *) fail "this guest is not Mac OS X 10.9" ;; esac
[ -x "$helper" ] || fail "no mavergreen helper at $helper"
sudo -n true || fail "the guest's user cannot sudo without a password, and installer needs root"
[ -f "$pkg" ] || fail "no $pkg"
! pkgutil --pkgs | grep -q '^dev\.mavergreen\.longboat' || fail "the guest already has a Longboat receipt"

step "install $pkg"
sudo installer -pkg "$pkg" -target /
got=$(pkgutil --pkg-info dev.mavergreen.longboat | awk '$1 == "version:" { print $2 }')
[ "$got" = "$ver" ] || fail "the installed dev.mavergreen.longboat is '$got', want $ver"

step "longboat list"
"$lb" list | tee list.out
grep -qx '10.9     available  available' list.out || fail "list does not offer 10.9's SDK and media"

step "longboat sdk list"
"$lb" sdk list | tee list.out
grep -qx '10.9 sdk109 available' list.out || fail "sdk list does not offer 10.9 as available"

step "longboat media list"
"$lb" media list | tee list.out
grep -qx '10.6 bring-your-own' list.out && grep -qx '10.9 recipe' list.out \
  || fail "media list does not say 10.6 is bring-your-own and 10.9 has a recipe"

step "sudo longboat sdk install 10.9"
path=$(sudo "$lb" sdk install 10.9) || fail "longboat sdk install 10.9 failed"
[ "$path" = "$sdk" ] || fail "sdk install printed '$path', want $sdk"
[ -f "$sdk/SDKSettings.plist" ] || fail "no $sdk/SDKSettings.plist"
printf '%s\n' '{"CanonicalName":"macosx10.9","MaximumDeploymentTarget":"10.9","Version":"10.9"}' \
  | cmp -s - "$sdk/SDKSettings.json" || fail "$sdk/SDKSettings.json is missing or is not shipyard's"
[ -f "$sdk/usr/include/stdio.h" ] || fail "no $sdk/usr/include/stdio.h"
"$lb" sdk list | tee list.out
grep -qx '10.9 sdk109 installed' list.out || fail "sdk list does not say 10.9 is installed"

step "sudo longboat sdk uninstall 10.9"
sudo "$lb" sdk uninstall 10.9
[ ! -e "$MG/sdk109" ] || fail "$MG/sdk109 is left behind"
! pkgutil --pkgs | grep -qx 'dev\.mavergreen\.longboat\.sdk109' || fail "the sdk109 receipt is left behind"

step "sudo mavergreen uninstall longboat"
sudo "$helper" uninstall longboat
[ ! -e "$MG/longboat" ] || fail "$MG/longboat is left behind"
[ ! -e "$lb" ] || fail "$lb is left behind"
! pkgutil --pkgs | grep -q '^dev\.mavergreen\.longboat' || fail "a Longboat receipt is left behind: $(pkgutil --pkgs | grep '^dev\.mavergreen\.longboat')"
rm -f list.out
step "ok: $pkg installs, installs the 10.9 SDK, and uninstalls cleanly on $(sw_vers -productVersion)"
