#!/bin/sh
# platform: macOS-only -- builds a fixture shipyard pkg with pkgbuild, which digest-bot.sh fill expands with pkgutil
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d "${TMPDIR:-/tmp}/longboat bot.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

R="$T/repo"
mkdir -p "$R/build" "$R/components/shipyard"
cp "$root"/build/*.sh "$root/build/shipyard-scripts.list" "$R/build/"
cp "$root/components/shipyard/version" "$R/components/shipyard/version"
BOT="$R/build/digest-bot.sh"
LIST="$R/build/shipyard-scripts.list"
COMMITTED="$R/components/shipyard/scripts.sha256"
unset LONGBOAT_SCRIPTS_SHA256 2>/dev/null || true

SCRIPTS=usr/local/mavergreen/shipyard/share/cmake/MavericksShipyard/scripts
while IFS= read -r p; do
  [ -n "$p" ] || continue
  mkdir -p "$T/root/$SCRIPTS/$(dirname "$p")"
  printf 'fixture %s\n' "$p" > "$T/root/$SCRIPTS/$p"
  printf '%s  %s\n' "$(shasum -a 256 < "$T/root/$SCRIPTS/$p" | cut -c1-64)" "$p" >> "$T/good"
done < "$LIST"
pkgbuild --quiet --root "$T/root" --identifier test.fixture --version 1 "$T/fixture.pkg" >/dev/null 2>&1
printf '%s  fixture.pkg\n' "$(shasum -a 256 < "$T/fixture.pkg" | cut -c1-64)" > "$T/SHA256SUMS"
LONGBOAT_SHIPYARD_PKG="$T/fixture.pkg"
LONGBOAT_SHIPYARD_SUMS="$T/SHA256SUMS"
export LONGBOAT_SHIPYARD_PKG LONGBOAT_SHIPYARD_SUMS
printf '%s\n' "0000  fetch_sdk.sh" > "$T/stale"

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

test_fill_prints_nothing_when_current() {
  cp "$T/good" "$COMMITTED"
  out=$(sh "$BOT" fill 2>"$T/err") || { cat "$T/err" >&2; return 1; }
  [ -z "$out" ] || { echo "fill printed [$out] for a current digest" >&2; return 1; }
}

test_fill_prints_the_new_digest_when_stale() {
  cp "$T/stale" "$COMMITTED"
  sh "$BOT" fill > "$T/out" 2>"$T/err" || { cat "$T/err" >&2; return 1; }
  cmp -s "$T/out" "$T/good" || { echo "fill printed:" >&2; cat "$T/out" >&2; return 1; }
  cmp -s "$COMMITTED" "$T/stale" || { echo "fill wrote the committed digest; only apply may" >&2; return 1; }
}

test_fill_fails_printing_nothing_on_an_error() {
  cp "$T/stale" "$COMMITTED"
  printf '%s  fixture.pkg\n' 0000000000000000000000000000000000000000000000000000000000000000 > "$T/badsums"
  rc=0
  LONGBOAT_SHIPYARD_SUMS="$T/badsums" sh "$BOT" fill > "$T/out" 2>/dev/null || rc=$?
  [ "$rc" -eq 1 ] || { echo "fill exited $rc on a check error, want 1" >&2; return 1; }
  [ ! -s "$T/out" ] || { echo "fill printed on a check error: $(cat "$T/out")" >&2; return 1; }
  rm "$COMMITTED"
  rc=0
  sh "$BOT" fill > "$T/out" 2>/dev/null || rc=$?
  [ "$rc" -eq 1 ] || { echo "fill exited $rc with no committed digest (check exits 1), want 1" >&2; return 1; }
  [ ! -s "$T/out" ] || { echo "fill read check's 1 as stale and printed: $(cat "$T/out")" >&2; return 1; }
}

test_apply_reports_changed_then_unchanged() {
  cp "$T/stale" "$COMMITTED"
  [ "$(sh "$BOT" apply "$T/good")" = changed ] || return 1
  cmp -s "$COMMITTED" "$T/good" || { echo "apply did not write the digest" >&2; return 1; }
  [ "$(sh "$BOT" apply "$T/good")" = unchanged ] || return 1
  cmp -s "$COMMITTED" "$T/good"
}

test_apply_refuses_a_malformed_digest_writing_nothing() {
  sed '1s/^./g/' "$T/good" > "$T/nothex"
  sed '1s/^.//' "$T/good" > "$T/short"
  sed '1d' "$T/good" > "$T/missing"
  { cat "$T/good"; printf '%s  extra.sh\n' "$(sed -n '1s/ .*//p' "$T/good")"; } > "$T/extra"
  { sed -n '2p' "$T/good"; sed -n '1p' "$T/good"; sed '1,2d' "$T/good"; } > "$T/reordered"
  sed '1s/  / /' "$T/good" > "$T/onespace"
  : > "$T/empty"
  cp "$T/stale" "$COMMITTED"
  for bad in nothex short missing extra reordered onespace empty does-not-exist; do
    if sh "$BOT" apply "$T/$bad" >/dev/null 2>&1; then echo "apply accepted $bad" >&2; return 1; fi
    cmp -s "$COMMITTED" "$T/stale" || { echo "a refused $bad wrote the committed digest" >&2; return 1; }
  done
}

test_usage_errors_exit_2() {
  for args in "" "fill extra" "apply" "apply a b" "frobnicate"; do
    rc=0
    # shellcheck disable=SC2086
    sh "$BOT" $args >/dev/null 2>&1 || rc=$?
    [ "$rc" -eq 2 ] || { echo "[$args] exited $rc, want 2" >&2; return 1; }
  done
}

check test_fill_prints_nothing_when_current
check test_fill_prints_the_new_digest_when_stale
check test_fill_fails_printing_nothing_on_an_error
check test_apply_reports_changed_then_unchanged
check test_apply_refuses_a_malformed_digest_writing_nothing
check test_usage_errors_exit_2
exit "$fail"
