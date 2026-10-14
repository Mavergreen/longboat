#!/bin/sh
# platform: host-agnostic
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/build/install-go.sh"
T="$(mktemp -d "${TMPDIR:-/tmp}/longboat-install-go.XXXXXX")"
trap 'rm -rf "$T"' EXIT
fail=0
A=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
B=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
C=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc

printf '%s  build-info-cross.txt\n%s  golang-1.26.9-cross-mavericks.1.pkg\n%s  golang-1.26.9-native-mavericks.1.pkg\n' \
  "$A" "$B" "$C" > "$T/golang"
printf '%s  go126-1.26.4-cross-mavericks.3.pkg\n%s  go126-1.26.4-native-mavericks.3.pkg\n' "$B" "$C" > "$T/go126"
printf '%s  go126-1.26.5-cross-mavericks.1.pkg\n%s  golang-1.26.5-cross-mavericks.1.pkg\n' "$A" "$B" > "$T/both"
printf '%s  golang-1.26.9-native-mavericks.1.pkg\n%s  golang-1.26.8-cross-mavericks.1.pkg\n%s  golang-1.26.9-cross-mavericks.10.pkg\n' \
  "$A" "$B" "$C" > "$T/none"

selects() {
  sums=$1; ver=$2; want=$3
  got="$(sh "$SCRIPT" --select "$T/$sums" "$ver")" \
    || { echo "--select $sums $ver failed" >&2; return 1; }
  [ "$got" = "$want" ] || { echo "--select $sums $ver printed '$got', want '$want'" >&2; return 1; }
}

selects_golang_prefix() { selects golang 1.26.9-mavericks.1 "$B  golang-1.26.9-cross-mavericks.1.pkg"; }
selects_go126_prefix() { selects go126 1.26.4-mavericks.3 "$B  go126-1.26.4-cross-mavericks.3.pkg"; }
prefers_golang_prefix() { selects both 1.26.5-mavericks.1 "$B  golang-1.26.5-cross-mavericks.1.pkg"; }

missing_pkg_exits_1() {
  rc=0
  sh "$SCRIPT" --select "$T/none" 1.26.9-mavericks.1 >"$T/out" 2>"$T/err" || rc=$?
  [ "$rc" -eq 1 ] || { echo "a SUMS without the cross pkg exited $rc, want 1" >&2; return 1; }
  [ ! -s "$T/out" ] || { echo "a SUMS without the cross pkg printed '$(cat "$T/out")'" >&2; return 1; }
  grep -q '1.26.9-mavericks.1' "$T/err" || { echo "the refusal does not name the version: $(cat "$T/err")" >&2; return 1; }
}

bad_usage_exits_2() {
  for args in "--select" "--select $T/golang" "--select $T/golang 1.26.9" "" "a b"; do
    rc=0
    # shellcheck disable=SC2086
    sh "$SCRIPT" $args >/dev/null 2>&1 || rc=$?
    [ "$rc" -eq 2 ] || { echo "install-go.sh $args exited $rc, want 2" >&2; return 1; }
  done
}

# spec: install-go.sh's usage -- the install path asks the registry for the repo, picks the asset with
#       --select, and hands it to fetch_product_pkg.sh; every stub refuses anything else, so nothing
#       here reaches the host's sudo or the network
install_goes_through_fetch_product_pkg() {
  S="$T/shipyard"; B="$T/stubs"; R="$T/root"
  mkdir -p "$S" "$B" "$R"
  cat > "$S/product-name.sh" <<'EOS'
[ "$1 $2" = "repo go126-cross" ] || exit 1
echo golang-126
EOS
  cat > "$S/fetch_product_pkg.sh" <<EOS
printf '%s\n' "\$*" > "$T/fetch.args"
mkdir -p "\$5/bin"
printf '#!/bin/sh\necho go version stub\n' > "\$5/bin/go"
chmod +x "\$5/bin/go"
echo "\$5"
EOS
  cat > "$B/curl" <<EOS
#!/bin/sh
out=""; url=""
while [ \$# -gt 0 ]; do
  case "\$1" in -o) out=\$2; shift ;; -*) ;; *) url=\$1 ;; esac
  shift
done
printf '%s\n' "\$url" >> "$T/curl.urls"
[ "\$url" = https://github.com/Mavergreen/golang-126/releases/download/1.26.9-mavericks.1/SHA256SUMS ] || exit 22
cp "$T/golang" "\$out"
EOS
  cat > "$B/sudo" <<EOS
#!/bin/sh
printf '%s\n' "\$*" >> "$T/sudo.log"
case "\$1 \$2" in
  "env MAVERGREEN_PKG_CACHE="*/install-go.*/cache)
    [ "\$3" = sh ] && [ "\$4" = "$S/fetch_product_pkg.sh" ] || exit 99
    shift 3
    exec sh "\$@" ;;
  "rm -rf")
    case "\$3" in */install-go.*/cache) exit 0 ;; esac ;;
esac
echo "stub sudo refuses: \$*" >&2
exit 99
EOS
  chmod +x "$B/curl" "$B/sudo"
  out="$(PATH="$B:/usr/bin:/bin" SHIPYARD_SCRIPTS="$S" INSTALL_GO_ROOT="$R" \
    sh "$SCRIPT" 1.26.9-mavericks.1 2>"$T/install.err")" \
    || { echo "install-go.sh failed: $(cat "$T/install.err")" >&2; return 1; }
  [ "$out" = "$R/go126-cross/bin" ] || { echo "install-go.sh printed '$out', want $R/go126-cross/bin" >&2; return 1; }
  want="--asset golang-1.26.9-cross-mavericks.1.pkg go126-cross 1.26.9-mavericks.1 $R/go126-cross"
  [ "$(cat "$T/fetch.args")" = "$want" ] \
    || { echo "fetch_product_pkg.sh got '$(cat "$T/fetch.args")', want '$want'" >&2; return 1; }
  ! grep -q 'refuses' "$T/install.err" || { echo "a stub refused a call: $(cat "$T/install.err")" >&2; return 1; }
}

expect() {
  if "$1" 2>"$T/err1"; then echo "ok: $1"; else echo "FAIL: $1: $(cat "$T/err1")" >&2; fail=1; fi
}

expect selects_golang_prefix
expect selects_go126_prefix
expect prefers_golang_prefix
expect missing_pkg_exits_1
expect bad_usage_exits_2
expect install_goes_through_fetch_product_pkg
exit "$fail"
