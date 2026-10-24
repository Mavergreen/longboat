#!/bin/sh
# platform: host-agnostic
# spec: src/longboat lb_media -- longboat's media list, media fetch and media verify
#       exec libexec/longboat/installmedia with their arguments and status intact; sdk fetch never does
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
for f in sdks.sh progress.sh fetch.sh install.sh uninstall.sh; do
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

test_media_fetch_execs_installmedia() {
  lb media fetch 10.9 --into "$T/D"
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed fetch 10.9 --into "$T/D"
}

test_media_verify_execs_installmedia() {
  lb media verify 10.6 x
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed verify 10.6 x
}

test_media_list_execs_installmedia() {
  lb media list
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed list
}

test_media_help_is_installmedias() {
  lb media --help
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed --help
}

test_arguments_pass_through_intact() {
  lb media fetch 10.6 --into "$T/a dir" --source-file "$T/my disc.img" --source-command "$T/make*disc"
  [ "$rc" -eq 7 ] || { echo "exit $rc, want installmedia's 7" >&2; return 1; }
  passed fetch 10.6 --into "$T/a dir" --source-file "$T/my disc.img" --source-command "$T/make*disc"
}

# spec: src/longboat lb_media, media_usage -- the media word carries only its own verbs; misuse prints its
#       usage on stderr, exit 2, and runs nothing
test_media_without_verb_is_usage() {
  for form in "media" "media frob" "media install 10.9" "media --help x"; do
    lb $form
    [ "$rc" -eq 2 ] && [ ! -f "$T/argv" ] && [ ! -s "$T/out" ] && grep -q 'longboat media list' "$T/err" \
      || { echo "longboat $form exited $rc, ran installmedia: $([ -f "$T/argv" ] && echo yes || echo no)" >&2; return 1; }
  done
}

# spec: src/longboat media_usage -- the usage longboat prints without running installmedia is installmedia's
#       own synopsis, word for word, so the two copies cannot drift
test_media_usage_is_installmedias_synopsis() {
  sed -n '/^const synopsis = `/,/^`/p' "$root/installmedia/cmd/installmedia/main.go" \
    | sed -e 's/^const synopsis = `//' -e 's/`$//' | tr -s ' \n' '  ' > "$T/want"
  lb media frob
  tr -s ' \n' '  ' < "$T/err" > "$T/got"
  [ -s "$T/want" ] && diff "$T/want" "$T/got" >&2
}

test_media_never_runs_the_sdk_path() {
  lb media fetch 10.9 --into "$T/D"
  passed fetch 10.9 --into "$T/D" && ! grep -q 'SDK' "$T/err"
}

test_sdk_fetch_never_calls_installmedia() {
  lb sdk fetch 10.9 --into "$T/D"
  [ ! -f "$T/argv" ] || { echo "longboat sdk fetch 10.9 ran installmedia" >&2; return 1; }
  [ "$rc" -ne 7 ]
}

test_a_missing_installmedia_is_named() {
  mv "$T/lb/libexec/longboat/installmedia" "$T/installmedia.away"
  lb media verify 10.6 x
  mv "$T/installmedia.away" "$T/lb/libexec/longboat/installmedia"
  [ "$rc" -eq 1 ] && grep -q 'installmedia' "$T/err" \
    || { echo "without installmedia, longboat media verify exited $rc: $(cat "$T/err")" >&2; return 1; }
}

# spec: src/longboat -- install builds and installs a .pkg, which only macOS can; a uname stub on PATH
#       stands in for each host, and only uname -s is read
uname_is() {
  mkdir -p "$T/uname-$1"
  printf '#!/bin/sh\necho %s\n' "$1" > "$T/uname-$1/uname"
  chmod 755 "$T/uname-$1/uname"
}
uname_is Linux
uname_is Darwin

test_install_off_macos_is_refused() {
  for form in "sdk install 10.9" "sdk install 10.9 --pkg-only $T/D" "sdk install"; do
    PATH="$T/uname-Linux:$PATH" lb $form
    [ "$rc" -eq 2 ] && [ ! -f "$T/argv" ] && [ ! -s "$T/out" ] && grep -qx 'longboat sdk install needs macOS for now: it builds and installs a .pkg' "$T/err" \
      && [ ! -e "$T/D" ] \
      || { echo "off macOS, longboat $form exited $rc: $(cat "$T/err")" >&2; return 1; }
  done
}

test_install_on_macos_is_not_refused() {
  PATH="$T/uname-Darwin:$PATH" lb sdk install
  [ "$rc" -eq 2 ] && grep -q 'usage: longboat sdk install' "$T/err" && ! grep -q 'needs macOS' "$T/err" \
    || { echo "on macOS, longboat sdk install exited $rc: $(cat "$T/err")" >&2; return 1; }
}

test_media_works_off_macos() {
  PATH="$T/uname-Linux:$PATH" lb media verify 10.6 x
  [ "$rc" -eq 7 ] || { echo "off macOS, media verify exited $rc, want installmedia's 7" >&2; return 1; }
  passed verify 10.6 x
}

for t in test_media_fetch_execs_installmedia test_media_verify_execs_installmedia test_media_list_execs_installmedia \
    test_media_help_is_installmedias test_arguments_pass_through_intact test_media_without_verb_is_usage \
    test_media_usage_is_installmedias_synopsis test_media_never_runs_the_sdk_path test_sdk_fetch_never_calls_installmedia test_a_missing_installmedia_is_named \
    test_install_off_macos_is_refused test_install_on_macos_is_not_refused test_media_works_off_macos; do
  check "$t"
done
exit "$fail"
