#!/bin/sh
# platform: host-agnostic
set -eu
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WF="$ROOT/.github/workflows"
REL="$WF/release.yml"
BOT="$WF/shipyard-digest-bot.yml"
REC="$WF/reconcile.yml"
T="$(mktemp -d "${TMPDIR:-/tmp}/longboat-wf.XXXXXX")"
trap 'rm -rf "$T"' EXIT
fail=0
for f in "$REL" "$BOT" "$REC"; do
  [ -f "$f" ] || { echo "FAIL: no $f" >&2; fail=1; }
done
[ "$fail" -eq 0 ] || exit 1

code() { grep -v '^[[:space:]]*#' "$1" || true; }
job() {
  code "$1" | awk -v j="  $2:" '/^jobs:/ { J = 1; next } /^[^ ]/ { J = 0 } J && $0 == j { p = 1; next }
                                 p && (/^[^ ]/ || /^  [^ ]/) { p = 0 } p'
}

no_pull_request_target() {
  ! code "$1" | grep -q 'pull_request_target' \
    || { echo "pull_request_target would hand a fork PR this repo's secrets" >&2; return 1; }
}

top_level_contents_read() {
  awk '/^permissions:/ { p = 1; next } p && /^[^ ]/ { p = 0 } p' "$1" | grep -qx '  contents: read' \
    || { echo "top-level permissions must be contents: read; only publish writes" >&2; return 1; }
}

publish_writes_contents() {
  job "$1" publish | grep -q 'contents: write' \
    || { echo "publish must declare its own contents: write" >&2; return 1; }
}

every_checkout_drops_credentials() {
  nco="$(code "$1" | grep -c 'uses: actions/checkout@' || true)"
  npc="$(code "$1" | grep -c 'persist-credentials: false' || true)"
  [ "$nco" = "$npc" ] \
    || { echo "every checkout needs persist-credentials: false ($nco checkouts, $npc without credentials)" >&2; return 1; }
}

ver_step_asks_release_needed_on_push_too() {
  job "$1" ver > "$T/ver"
  grep -q '"$SHIPYARD_SCRIPTS/release-needed.sh"' "$T/ver" \
    || { echo "the ver job must decide by release-needed.sh: the digest is the declared state" >&2; return 1; }
  grep -q '"$GITHUB_EVENT_NAME" = push' "$T/ver" \
    || { echo "a push to main must reach release-needed.sh too, so a digest change releases on merge" >&2; return 1; }
}

no_tags_trigger() {
  for f in "$@"; do
    if code "$f" | grep -Eq '^[[:space:]]*tags(-ignore)?:'; then
      echo "$(basename "$f") has a tags: trigger; tags are retired as a release input" >&2
      return 1
    fi
  done
}

tests_step_can_extract_the_pin() {
  job "$1" build | awk '/^      - / { s = ($0 == "      - name: Tests") } s' > "$T/tests-step"
  [ -s "$T/tests-step" ] || { echo "the build job has no step named Tests" >&2; return 1; }
  grep -Eq '^          GH_TOKEN: \$\{\{ github\.token \}\}$' "$T/tests-step" \
    || { echo "the Tests step needs GH_TOKEN, so install-test can extract the pinned shipyard and test install against the scripts Longboat ships" >&2; return 1; }
}

build_checks_the_digest() {
  job "$1" build | grep -Eq '^[[:space:]]*(- )?run: sh build/shipyard-scripts\.sh check[[:space:]]*$' \
    || { echo "the build job must run sh build/shipyard-scripts.sh check, so a stale digest cannot merge" >&2; return 1; }
}

linux_job_runs_repo_tests() {
  job "$1" linux > "$T/linux"
  [ -s "$T/linux" ] || { echo "release.yml has no linux job" >&2; return 1; }
  grep -qx '    runs-on: ubuntu-latest' "$T/linux" \
    || { echo "the linux job must run on ubuntu-latest: it proves the host-agnostic tests and installmedia work off macOS" >&2; return 1; }
  grep -Eq '^[[:space:]]*(- )?run: sh "\$SHIPYARD_SCRIPTS/run-repo-tests\.sh"[[:space:]]*$' "$T/linux" \
    || { echo "the linux job must run sh \"\$SHIPYARD_SCRIPTS/run-repo-tests.sh\", or nothing tests installmedia on Linux" >&2; return 1; }
  job "$1" collect | grep -Eq '^    needs: \[(.*, )?linux(,|\])' \
    || { echo "collect must need the linux job, so a release waits for installmedia's Linux tests" >&2; return 1; }
}

step_of() {
  awk -v re="$2" '/^      - / { n++ } n && $0 ~ re { print n; f = 1; exit } END { if (!f) print 0 }' "$1"
}

build_makes_installmedia_before_package() {
  job "$1" build > "$T/build-order"
  go="$(step_of "$T/build-order" 'sh build/install-go\.sh')"
  bd="$(step_of "$T/build-order" 'sh build/build-installmedia\.sh darwin ')"
  bl="$(step_of "$T/build-order" 'sh build/build-installmedia\.sh linux ')"
  ct="$(step_of "$T/build-order" 'sh tests/installmedia-compat-test\.sh')"
  pk="$(step_of "$T/build-order" 'sh build/package\.sh')"
  for s in "go:install-go.sh" "bd:build-installmedia.sh darwin" "bl:build-installmedia.sh linux" \
           "ct:installmedia-compat-test.sh" "pk:package.sh"; do
    eval "n=\$${s%%:*}"
    [ "$n" -gt 0 ] || { echo "the build job has no step running ${s#*:}" >&2; return 1; }
  done
  [ "$go" -lt "$bd" ] && [ "$go" -lt "$bl" ] \
    || { echo "Install the pinned Go must come before Build installmedia, which builds with that Go" >&2; return 1; }
  [ "$bd" -lt "$ct" ] \
    || { echo "the installmedia compat step reads the darwin build, so it must follow Build installmedia" >&2; return 1; }
  [ "$ct" -lt "$pk" ] && [ "$bl" -lt "$pk" ] \
    || { echo "Package ships installmedia, so it must follow its build and the compat step" >&2; return 1; }
}

reconcile_dispatches_a_declared_input() {
  key="$(code "$1" | sed -n 's/^[[:space:]]*dispatch-field:[[:space:]]*\([A-Za-z_-]*\)=.*/\1/p')"
  [ -n "$key" ] || key=local_release
  awk '/^  workflow_dispatch:/ { d = 1; next }
       d && /^    inputs:/ { i = 1; next }
       i && /^      [^ ]/ { s = $0; sub(/^ */, "", s); sub(/:.*/, "", s); print s; next }
       i && /^        / { next }
       /^ *$/ { next }
       { d = 0; i = 0 }' "$2" \
    | grep -qx "$key" \
    || { echo "reconcile dispatches -f $key=..., which release.yml does not declare as a workflow_dispatch input" >&2; return 1; }
}

bot_digest_job_reads_only() {
  grep -qx 'permissions: {}' "$1" || { echo "the bot's top-level permissions must be {}" >&2; return 1; }
  perms="$(job "$1" digest | awk '/^    permissions:/ { p = 1; print; next } p && /^      [^ ]/ { print; next } { p = 0 }')"
  [ "$perms" = "$(printf '    permissions:\n      contents: read')" ] \
    || { echo "the digest job reads shipyard's bytes, so it holds contents: read and nothing else; got: $perms" >&2; return 1; }
  job "$1" digest | grep -q 'persist-credentials: false' \
    || { echo "the digest job's checkout must not persist its token" >&2; return 1; }
}

bot_push_job_follows_digest() {
  job "$1" push | grep -qx '    needs: digest' \
    || { echo "the push job must run only after the digest job" >&2; return 1; }
  job "$1" push | grep -qx '      contents: write' \
    || { echo "the push job needs contents: write to push the digest" >&2; return 1; }
}

no_expressions_in_run_blocks() {
  awk '{ match($0, /^ */); ind = RLENGTH }
       inrun && (ind > runind || $0 ~ /^ *$/) { if (index($0, "${{")) { print NR ": " $0; bad = 1 } next }
       { inrun = 0 }
       /^ *- run: *[|>]/ { inrun = 1; runind = ind + 2; next }
       /^ *run: *[|>]/ { inrun = 1; runind = ind; next }
       /^ *(- )?run: / { if (index($0, "${{")) { print NR ": " $0; bad = 1 } }
       END { exit bad }' "$1" >&2 \
    || { echo "a run: block interpolates \${{ }}; pass values through env:" >&2; return 1; }
}

expect() {
  if "$@" 2>"$T/err"; then echo "ok: $1"; else echo "FAIL: $1: $(cat "$T/err")" >&2; fail=1; fi
}

refuses() {
  mutant=$1; shift
  if "$@" 2>/dev/null; then
    echo "FAIL: $1 accepted a workflow mutated to break it ($mutant)" >&2; fail=1
  else
    echo "ok: $1 refuses $mutant"
  fi
}

expect_on_mutant() {
  what=$1; shift
  if "$@" 2>"$T/err"; then echo "ok: $1 accepts $what"; else echo "FAIL: $1 refused $what: $(cat "$T/err")" >&2; fail=1; fi
}

move_step() {
  awk -v mv="      - name: $1" -v at="      - name: $2" '
    /^      - / || /^  [^ ]/ { inmv = ($0 == mv) }
    inmv { buf = buf $0 "\n"; next }
    { line[++n] = $0 }
    END { for (i = 1; i <= n; i++) { if (line[i] == at) printf "%s", buf; print line[i] } }'
}

mutant() {
  m="$T/$1.yml"; src=$2; shift 2
  "$@" < "$src" > "$m"
  if cmp -s "$src" "$m"; then echo "FAIL: mutant $m is unchanged; the mutation no longer applies" >&2; fail=1; fi
}

expect no_pull_request_target "$REL"
expect top_level_contents_read "$REL"
expect publish_writes_contents "$REL"
expect every_checkout_drops_credentials "$REL"
expect ver_step_asks_release_needed_on_push_too "$REL"
expect no_tags_trigger "$WF"/*.yml
expect build_checks_the_digest "$REL"
expect tests_step_can_extract_the_pin "$REL"
expect linux_job_runs_repo_tests "$REL"
expect build_makes_installmedia_before_package "$REL"
expect reconcile_dispatches_a_declared_input "$REC" "$REL"
expect bot_digest_job_reads_only "$BOT"
expect bot_push_job_follows_digest "$BOT"
expect no_expressions_in_run_blocks "$BOT"

mutant no-release-needed "$REL" sed 's/release-needed\.sh/release-unneeded.sh/'
refuses "a ver step without release-needed.sh" ver_step_asks_release_needed_on_push_too "$m"
mutant dispatch-only "$REL" sed 's/ || \[ "\$GITHUB_EVENT_NAME" = push \]//'
refuses "a ver step that asks only on dispatch" ver_step_asks_release_needed_on_push_too "$m"
mutant tags "$REL" awk '{ print } /^  push:$/ { print "    tags: [\"*\"]" }'
refuses "a tags: trigger" no_tags_trigger "$m"
mutant no-digest-check "$REL" sed '/shipyard-scripts\.sh check/d'
refuses "a build without the digest check" build_checks_the_digest "$m"
mutant tests-no-token "$REL" awk '/^      - / { s = ($0 == "      - name: Tests") } !(s && (/GH_TOKEN/ || /^        env:$/)) { print }'
refuses "a Tests step without GH_TOKEN" tests_step_can_extract_the_pin "$m"
mutant default-dispatch "$REC" sed '/dispatch-field/d'
refuses "reconcile's default local_release=true dispatch" reconcile_dispatches_a_declared_input "$m" "$REL"
mutant digest-writes "$BOT" sed 's/^      contents: read$/      contents: write/'
refuses "a digest job with contents: write" bot_digest_job_reads_only "$m"
mutant digest-actions "$BOT" awk '{ print } /^      contents: read$/ { print "      actions: write" }'
refuses "a digest job with a second permission" bot_digest_job_reads_only "$m"
mutant digest-persists "$BOT" sed '/persist-credentials: false/d'
refuses "a digest job whose checkout keeps its token" bot_digest_job_reads_only "$m"
mutant push-alone "$BOT" sed '/^    needs: digest$/d'
refuses "a push job that does not wait for the digest job" bot_push_job_follows_digest "$m"
mutant run-expr "$BOT" sed 's/"\$BRANCH"/"${{ github.ref_name }}"/g'
refuses "a run: block that interpolates \${{ }}" no_expressions_in_run_blocks "$m"
mutant linux-no-tests "$REL" awk '/^  [^ ]/ { j = $0 } !(j == "  linux:" && /run-repo-tests\.sh/) { print }'
refuses "a linux job that does not run the repo's tests" linux_job_runs_repo_tests "$m"
mutant collect-skips-linux "$REL" sed 's/^    needs: \[ver, build, linux\]$/    needs: [ver, build]/'
refuses "a collect that does not wait for the linux job" linux_job_runs_repo_tests "$m"
mutant collect-linux-first "$REL" sed 's/^    needs: \[ver, build, linux\]$/    needs: [linux, ver, build]/'
expect_on_mutant "collect needing linux first" linux_job_runs_repo_tests "$m"
mutant collect-linux-only "$REL" sed 's/^    needs: \[ver, build, linux\]$/    needs: [linux]/'
expect_on_mutant "collect needing only linux" linux_job_runs_repo_tests "$m"
mutant collect-linuxish "$REL" sed 's/^    needs: \[ver, build, linux\]$/    needs: [ver, build, linux-arm]/'
refuses "a collect that needs only a job named like linux" linux_job_runs_repo_tests "$m"
mutant linux-macos "$REL" awk '/^  [^ ]/ { j = $0 } { if (j == "  linux:") sub(/ubuntu-latest/, "macos-26"); print }'
refuses "a linux job that runs on macOS" linux_job_runs_repo_tests "$m"
mutant linux-checkout-creds "$REL" awk '/^  [^ ]/ { j = $0 } { if (j == "  linux:") sub(/persist-credentials: false/, "persist-credentials: true"); print }'
refuses "a linux checkout that keeps its token" every_checkout_drops_credentials "$m"
mutant checkout-creds "$REL" awk '!d && sub(/persist-credentials: false/, "persist-credentials: true") { d = 1 } { print }'
refuses "a release checkout that keeps its token" every_checkout_drops_credentials "$m"
mutant package-first "$REL" move_step Package "Install the pinned Go"
refuses "a Package step before installmedia is built" build_makes_installmedia_before_package "$m"
mutant go-late "$REL" move_step "Install the pinned Go" Package
refuses "a pinned Go installed after Build installmedia" build_makes_installmedia_before_package "$m"
mutant compat-first "$REL" move_step "installmedia imports nothing 10.9 lacks" "Build installmedia"
refuses "a compat step before the build it reads" build_makes_installmedia_before_package "$m"
mutant compat-late "$REL" move_step "installmedia imports nothing 10.9 lacks" Tests
refuses "a compat step after Package" build_makes_installmedia_before_package "$m"
mutant no-compat "$REL" sed '/installmedia-compat-test\.sh/d'
refuses "a build job without the compat step" build_makes_installmedia_before_package "$m"
mutant no-linux-build "$REL" sed '/build-installmedia\.sh linux/d'
refuses "a build job that builds no linux installmedia" build_makes_installmedia_before_package "$m"
exit "$fail"
