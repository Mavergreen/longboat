#!/bin/sh
# platform: host-agnostic
# spec: README.md "Fetching and verifying installers" -- longboat's fetch installer and verify installer
#       exec libexec/longboat/installmedia with their arguments and status intact; the SDK's fetch never does
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)

T=$(mktemp -d "${TMPDIR:-/tmp}/longboat-installmedia-cli.XXXXXX")
trap 'rm -rf "$T"' EXIT
fail=0

check() {
  if "$@"; then echo "ok: $1"; else echo "FAIL: $1" >&2; fail=1; fi
}

# spec: build/package.sh's installed layout, with a stub installmedia that only records its argv, one
#       argument per line, and exits 7, so each test sees the arguments and the status pass through
mkdir -p "$T/lb/bin" "$T/lb/libexec/longboat" "$T/empty-shipyard"
cp "$root/src/longboat" "$T/lb/bin/longboat"
for f in sdks.sh progress.sh fetch.sh install.sh; do
  cp "$root/src/$f" "$T/lb/libexec/longboat/$f"
done
cat > "$T/lb/libexec/longboat/installmedia" <<EOF
#!/bin/sh
for a; do printf '%s\n' "\$a"; done > "$T/argv"
exit 7
EOF
chmod 755 "$T/lb/libexec/longboat/installmedia"
LB="$T/lb/bin/longboat"
LONGBOAT_SHIPYARD_DIR="$T/empty-shipyard"
export LONGBOAT_SHIPYARD_DIR

lb() {
  rm -f "$T/argv"
  rc=0
  sh "$LB" "$@" >"$T/out" 2>"$T/err" || rc=$?
}

passed() {
  [ -f "$T/argv" ] || { echo "installmedia never ran (longboat exited $rc: $(cat "$T/err"))" >&2; return 1; }
  for a; do printf '%s\n' "$a"; done > "$T/want"
  diff "$T/want" "$T/argv" >&2
}

test_fetch_installer_execs_installmedia() {
  lb fetch installer 10.9 --into "$T/D"
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed fetch installer 10.9 --into "$T/D"
}

test_verify_installer_execs_installmedia() {
  lb verify installer 10.6 x
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed verify installer 10.6 x
}

test_arguments_pass_through_intact() {
  lb fetch installer 10.6 --into "$T/a dir" --source-file "$T/my disc.img" --source-command "$T/make*disc"
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed fetch installer 10.6 --into "$T/a dir" --source-file "$T/my disc.img" --source-command "$T/make*disc"
}

test_verify_without_installer_is_usage() {
  for form in "verify" "verify sdk 10.9"; do
    lb $form
    [ "$rc" -eq 2 ] && [ ! -f "$T/argv" ] && [ ! -s "$T/out" ] && grep -q 'usage:' "$T/err" \
      || { echo "longboat $form exited $rc, ran installmedia: $([ -f "$T/argv" ] && echo yes || echo no)" >&2; return 1; }
  done
}

test_sdk_fetch_never_calls_installmedia() {
  lb fetch 10.9 --into "$T/D"
  [ ! -f "$T/argv" ] || { echo "longboat fetch 10.9 ran installmedia" >&2; return 1; }
  [ "$rc" -ne 7 ]
}

test_help_lists_both_forms() {
  lb --help
  [ "$rc" -eq 0 ] && [ ! -s "$T/err" ] \
    && grep -q 'fetch installer <release> --into DIR' "$T/out" \
    && grep -q 'verify installer <release> <image>' "$T/out"
}

test_a_missing_installmedia_is_named() {
  mv "$T/lb/libexec/longboat/installmedia" "$T/installmedia.away"
  lb verify installer 10.6 x
  mv "$T/installmedia.away" "$T/lb/libexec/longboat/installmedia"
  [ "$rc" -eq 1 ] && grep -q 'installmedia' "$T/err" \
    || { echo "without installmedia, longboat verify installer exited $rc: $(cat "$T/err")" >&2; return 1; }
}

check test_fetch_installer_execs_installmedia
check test_verify_installer_execs_installmedia
check test_arguments_pass_through_intact
check test_verify_without_installer_is_usage
check test_sdk_fetch_never_calls_installmedia
check test_help_lists_both_forms
check test_a_missing_installmedia_is_named
exit "$fail"
