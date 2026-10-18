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

required_check_fails_when_ver_fails() {
  job "$1" build > "$T/build"
  grep -qx '    if: ${{ !cancelled() }}' "$T/build" \
    || { echo "build is the required check, and GitHub counts a skipped required check as passing: it must run even when ver fails" >&2; return 1; }
  awk '/^      - / { n++ } n == 1' "$T/build" > "$T/first-step"
  grep -qx '          VER: ${{ needs.ver.result }}' "$T/first-step" && grep -q '\[ "\$VER" = success \]' "$T/first-step" \
    || { echo "build's first step must fail unless ver succeeded, so a failed ver fails the required check" >&2; return 1; }
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
  ! grep -Eq '^    needs: (build|\[(.*, )?build(,|\]))' "$T/linux" \
    || { echo "the linux job must not need build: a macOS build failure would skip its Linux tests" >&2; return 1; }
}

# platform: the regex reaches awk through ENVIRON, not -v: -v applies string escapes first, so gawk and
#           busybox turn "\$" and "\." into a bare "$" and "." (gawk with a warning on stderr) and the
#           regex no longer means what it says; ENVIRON hands it over byte for byte in every awk
step_of() {
  STEP_RE="$2" awk 'BEGIN { re = ENVIRON["STEP_RE"] } /^      - / { n++ } n && $0 ~ re { print n; f = 1; exit } END { if (!f) print 0 }' "$1"
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

step_text() {
  awk -v n="$2" '/^      - / { i++ } i == n' "$1"
}

uploads_the_tarball() {
  job "$1" "$2" > "$T/up-$2"
  n="$(step_of "$T/up-$2" 'uses: actions/upload-artifact@')"
  [ "$n" -gt 0 ] || { echo "the $2 job uploads no artifact" >&2; return 1; }
  step_text "$T/up-$2" "$n" | grep -qx '            dist/\*\.tar\.gz' \
    || { echo "the $2 job's artifact must carry dist/*.tar.gz, the Linux tarball" >&2; return 1; }
}

build_packs_the_linux_tarball() {
  job "$1" build > "$T/build-linux"
  pk="$(step_of "$T/build-linux" '^[[:space:]]*(- )?run: sh build/package\.sh')"
  pl="$(step_of "$T/build-linux" 'sh build/package-linux\.sh')"
  [ "$pl" -gt 0 ] || { echo "the build job never runs build/package-linux.sh" >&2; return 1; }
  [ "$pk" -gt 0 ] && [ "$pk" -lt "$pl" ] \
    || { echo "package-linux.sh packs package.sh's stage, so it must follow the Package step" >&2; return 1; }
  step_text "$T/build-linux" "$pl" | grep -q 'SOURCE_DATE_EPOCH="$(git log -1 --format=%ct)"' \
    || { echo "package-linux.sh must run with SOURCE_DATE_EPOCH set to HEAD's commit time, or the tarball is not reproducible" >&2; return 1; }
  uploads_the_tarball "$1" build && uploads_the_tarball "$1" collect
}

# spec: README.md "On Linux" -- $1 is the tarball step with its continuation lines joined, its leading
#       blanks and its cat "$x/err" lines dropped; $2 is a command, and $3 the check that must follow it
followed_by() {
  CMD="$2" CHECK="$3" awk 'BEGIN { c = ENVIRON["CMD"]; e = ENVIRON["CHECK"] }
    prev == c && index($0, e) == 1 { ok = 1 } { prev = $0 } END { exit !ok }' "$1"
}

linux_job_smokes_the_tarball() {
  job "$1" linux-tarball > "$T/linux-smoke"
  [ -s "$T/linux-smoke" ] || { echo "release.yml has no linux-tarball job" >&2; return 1; }
  grep -qx '    runs-on: ubuntu-latest' "$T/linux-smoke" \
    || { echo "the linux-tarball job must run on ubuntu-latest: it proves the tarball runs on Linux" >&2; return 1; }
  grep -Eq '^    needs: \[(.*, )?build(,|\])' "$T/linux-smoke" \
    || { echo "the linux-tarball job must need build, whose artifact holds the tarball it runs" >&2; return 1; }
  job "$1" collect | grep -Eq '^    needs: \[(.*, )?linux-tarball(,|\])' \
    || { echo "collect must need the linux-tarball job, so a release waits for the tarball to run on Linux" >&2; return 1; }
  art="$(job "$1" build | sed -n 's/^          name: \(dist-[a-z]*\)$/\1/p' | head -1)"
  dl="$(step_of "$T/linux-smoke" 'uses: actions/download-artifact@')"
  [ "$dl" -gt 0 ] && [ -n "$art" ] && step_text "$T/linux-smoke" "$dl" | grep -q "name: $art[,} ]" \
    || { echo "the linux-tarball job must download the build job's artifact ($art)" >&2; return 1; }
  sm="$(step_of "$T/linux-smoke" 'tar -xzf .*longboat-\$VER-linux-x86_64\.tar\.gz')"
  [ "$sm" -gt "$dl" ] \
    || { echo "the linux-tarball job must extract the tarball after downloading it" >&2; return 1; }
  step_text "$T/linux-smoke" "$sm" | awk '
    { sub(/^[[:space:]]+/, "") }
    $0 == "cat \"$x/err\"" { next }
    buf != "" { $0 = buf " " $0; buf = "" }
    /\\$/ { sub(/[[:space:]]*\\$/, ""); buf = $0; next }
    { print }' > "$T/smoke"
  for want in 'lb="$x/opt/mavergreen/longboat/bin/longboat"' '"$lb" --help'; do
    grep -qxF -- "$want" "$T/smoke" \
      || { echo "the tarball step must run $want" >&2; return 1; }
  done
  followed_by "$T/smoke" 'rc=0; "$lb" verify installer 10.6 /dev/null 2>"$x/err" || rc=$?' \
    '[ "$rc" -eq 1 ] && grep -q '"'"'not a disc image'"'"' "$x/err" || {' \
    || { echo "the tarball step must run verify installer 10.6 /dev/null and want exit 1 and 'not a disc image'" >&2; return 1; }
  followed_by "$T/smoke" 'rc=0; "$lb" fetch installer 10.6 --into "$RUNNER_TEMP/x" || rc=$?' \
    '[ "$rc" -eq 2 ] || {' \
    || { echo "the tarball step must run fetch installer 10.6 with no source and want exit 2" >&2; return 1; }
  followed_by "$T/smoke" 'rc=0; "$lb" install 10.9 2>"$x/err" || rc=$?' \
    '[ "$rc" -eq 2 ] && grep -q '"'"'longboat install needs macOS'"'"' "$x/err" || {' \
    || { echo "the tarball step must run install 10.9 and want exit 2 and 'longboat install needs macOS'" >&2; return 1; }
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

bot_push_job_pushes_as_the_app() {
  job "$1" push > "$T/push"
  grep -qx '    needs: digest' "$T/push" \
    || { echo "the push job must run only after the digest job" >&2; return 1; }
  grep -q 'uses: actions/create-github-app-token@' "$T/push" \
    && grep -qx '          client-id: ${{ vars.PINS_BOT_CLIENT_ID }}' "$T/push" \
    && grep -qx '          private-key: ${{ secrets.PINS_BOT_PRIVATE_KEY }}' "$T/push" \
    && grep -qx '          permission-contents: write' "$T/push" \
    || { echo "the push job must mint the pins-bot App's token: a GITHUB_TOKEN push starts no check that counts for the PR" >&2; return 1; }
  grep -qx '          token: ${{ steps.app-token.outputs.token }}' "$T/push" \
    || { echo "the push job's checkout must push with the App's token" >&2; return 1; }
  ! grep -Eq '^      (contents|actions): write$' "$T/push" \
    || { echo "the push job's GITHUB_TOKEN writes nothing: the App's token does the pushing" >&2; return 1; }
  ! grep -q 'gh workflow run' "$T/push" \
    || { echo "a dispatched build does not count for the PR's required check; the App's push starts the real one" >&2; return 1; }
}

# platform: the App's push starts a bot run of its own; in the push run's concurrency group it would
#           cancel that run just after its push, and it has nothing to check (its push is the digest)
bot_skips_its_own_push() {
  grep -Fqx "  group: shipyard-digest-bot-\${{ github.ref }}\${{ github.actor == 'mavergreen-pins-bot[bot]' && format('-{0}', github.run_id) || '' }}" "$1" \
    || { echo "the run the App's push starts needs a concurrency group of its own, or it cancels the run that pushed" >&2; return 1; }
  job "$1" digest | grep -qx "    if: github.actor != 'mavergreen-pins-bot\[bot\]'" \
    || { echo "the digest job must skip the run the App's own push starts" >&2; return 1; }
}

# spec: INGREDIENTS.md "The shipyard scripts digest" -- fill only downloads, unpacks and hashes shipyard's
#       pkg, so it runs on Linux and spends none of the org's macOS runners; bsdtar unpacks the pkg there
bot_digest_job_runs_on_linux() {
  job "$1" digest > "$T/digest"
  grep -qx '    runs-on: ubuntu-latest' "$T/digest" \
    || { echo "the digest job must run on ubuntu-latest: it needs no macOS, and macOS runners are scarce" >&2; return 1; }
  awk '/libarchive-tools/ { a = NR } /digest-bot\.sh fill/ { f = NR } END { exit !(a && f && a < f) }' "$T/digest" \
    || { echo "the digest job must install libarchive-tools (bsdtar) before digest-bot.sh fill unpacks the pkg" >&2; return 1; }
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
expect required_check_fails_when_ver_fails "$REL"
expect tests_step_can_extract_the_pin "$REL"
expect linux_job_runs_repo_tests "$REL"
expect build_makes_installmedia_before_package "$REL"
expect build_packs_the_linux_tarball "$REL"
expect linux_job_smokes_the_tarball "$REL"
expect reconcile_dispatches_a_declared_input "$REC" "$REL"
expect bot_digest_job_reads_only "$BOT"
expect bot_push_job_pushes_as_the_app "$BOT"
expect bot_skips_its_own_push "$BOT"
expect bot_digest_job_runs_on_linux "$BOT"
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
refuses "a push job that does not wait for the digest job" bot_push_job_pushes_as_the_app "$m"
mutant push-github-token "$BOT" sed 's/steps\.app-token\.outputs\.token/github.token/'
refuses "a push job whose checkout pushes with GITHUB_TOKEN" bot_push_job_pushes_as_the_app "$m"
mutant push-dispatch "$BOT" awk '{ print } /git push origin/ { print "          gh workflow run release.yml --ref \"$BRANCH\"" }'
refuses "a push job that dispatches release.yml" bot_push_job_pushes_as_the_app "$m"
mutant push-writes "$BOT" awk '{ print } /^    needs: digest$/ { print "    permissions:"; print "      contents: write" }'
refuses "a push job whose GITHUB_TOKEN can write" bot_push_job_pushes_as_the_app "$m"
mutant own-group "$BOT" sed "s/\\\${{ github.actor == 'mavergreen-pins-bot\\[bot\\]' && format('-{0}', github.run_id) || '' }}\$//"
refuses "a bot whose own push shares the group that cancels" bot_skips_its_own_push "$m"
mutant own-run "$BOT" sed "/^    if: github.actor != 'mavergreen-pins-bot\\[bot\\]'\$/d"
refuses "a digest job that runs on the App's own push" bot_skips_its_own_push "$m"
mutant digest-on-macos "$BOT" awk '!d && $0 == "    runs-on: ubuntu-latest" { print "    runs-on: macos-26"; d = 1; next } { print }'
refuses "a digest job on a macOS runner" bot_digest_job_runs_on_linux "$m"
mutant digest-no-bsdtar "$BOT" sed '/libarchive-tools/d'
refuses "a digest job that unpacks the pkg without installing bsdtar" bot_digest_job_runs_on_linux "$m"
mutant build-skips "$REL" sed '/^    if: \${{ !cancelled() }}$/d'
refuses "a required build that is skipped when ver fails" required_check_fails_when_ver_fails "$m"
mutant build-no-guard "$REL" sed 's/needs\.ver\.result/needs.ver.outputs.version/'
refuses "a required build that runs on whatever ver left" required_check_fails_when_ver_fails "$m"
mutant run-expr "$BOT" sed 's|refs/heads/\$BRANCH|refs/heads/${{ github.ref_name }}|'
refuses "a run: block that interpolates \${{ }}" no_expressions_in_run_blocks "$m"
mutant linux-no-tests "$REL" awk '/^  [^ ]/ { j = $0 } !(j == "  linux:" && /run-repo-tests\.sh/) { print }'
refuses "a linux job that does not run the repo's tests" linux_job_runs_repo_tests "$m"
mutant collect-skips-linux "$REL" sed 's/^    needs: \[ver, build, linux, linux-tarball\]$/    needs: [ver, build, linux-tarball]/'
refuses "a collect that does not wait for the linux job" linux_job_runs_repo_tests "$m"
mutant collect-linux-first "$REL" sed 's/^    needs: \[ver, build, linux, linux-tarball\]$/    needs: [linux, ver, build, linux-tarball]/'
expect_on_mutant "collect needing linux first" linux_job_runs_repo_tests "$m"
mutant collect-linux-only "$REL" sed 's/^    needs: \[ver, build, linux, linux-tarball\]$/    needs: [linux, linux-tarball]/'
expect_on_mutant "collect needing only linux" linux_job_runs_repo_tests "$m"
mutant collect-linuxish "$REL" sed 's/^    needs: \[ver, build, linux, linux-tarball\]$/    needs: [ver, build, linux-arm, linux-tarball]/'
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
mutant no-package-linux "$REL" sed '/package-linux\.sh/d'
refuses "a build that never packs the Linux tarball" build_packs_the_linux_tarball "$m"
mutant package-linux-first "$REL" move_step "Package for Linux" Package
refuses "a Linux tarball packed before Package stages it" build_packs_the_linux_tarball "$m"
mutant no-epoch "$REL" sed 's/SOURCE_DATE_EPOCH="\$(git log -1 --format=%ct)"/SOURCE_DATE_EPOCH="$(date +%s)"/'
refuses "a Linux tarball stamped with the time of the build" build_packs_the_linux_tarball "$m"
mutant build-artifact-no-tarball "$REL" awk '/^  [^ ]/ { j = $0 } !(j == "  build:" && /dist\/\*\.tar\.gz/) { print }'
refuses "a build artifact without the tarball" build_packs_the_linux_tarball "$m"
mutant collect-artifact-no-tarball "$REL" awk '/^  [^ ]/ { j = $0 } !(j == "  collect:" && /dist\/\*\.tar\.gz/) { print }'
refuses "a release artifact without the tarball" build_packs_the_linux_tarball "$m"
mutant tarball-before-build "$REL" sed 's/^    needs: \[ver, build\]$/    needs: ver/'
refuses "a linux-tarball job that does not wait for the build's tarball" linux_job_smokes_the_tarball "$m"
mutant linux-needs-build "$REL" awk '/^  [^ ]/ { j = $0 } { if (j == "  linux:") sub(/^    needs: ver$/, "    needs: [ver, build]"); print }'
refuses "a linux job that waits for the macOS build" linux_job_runs_repo_tests "$m"
mutant collect-skips-tarball "$REL" sed 's/^    needs: \[ver, build, linux, linux-tarball\]$/    needs: [ver, build, linux]/'
refuses "a collect that does not wait for the linux-tarball job" linux_job_smokes_the_tarball "$m"
mutant tarball-no-download "$REL" awk '/^  [^ ]/ { j = $0 } !(j == "  linux-tarball:" && /download-artifact/) { print }'
refuses "a linux-tarball job that downloads no artifact" linux_job_smokes_the_tarball "$m"
mutant tarball-other-artifact "$REL" awk '/^  [^ ]/ { j = $0 } { if (j == "  linux-tarball:") sub(/name: dist-longboat/, "name: dist-other"); print }'
refuses "a linux-tarball job that downloads another artifact" linux_job_smokes_the_tarball "$m"
mutant verify-wants-0 "$REL" sed 's/-eq 1 \] && grep -q .not a disc image./-eq 0 ] \&\& grep -q "not a disc image"/'
refuses "a verify that wants exit 0" linux_job_smokes_the_tarball "$m"
mutant fetch-wants-0 "$REL" sed 's/\[ "\$rc" -eq 2 \] || { echo "::error::fetch/[ "$rc" -eq 0 ] || { echo "::error::fetch/'
refuses "a fetch with no source that wants exit 0" linux_job_smokes_the_tarball "$m"
mutant install-wants-0 "$REL" sed 's/-eq 2 \] && grep -q .longboat install needs macOS./-eq 0 ] \&\& grep -q "longboat install needs macOS"/'
refuses "an install that wants exit 0" linux_job_smokes_the_tarball "$m"
mutant no-install "$REL" sed '/"\$lb" install 10\.9/d'
refuses "a tarball step that never runs install" linux_job_smokes_the_tarball "$m"
mutant smoke-first "$REL" move_step "The Linux tarball runs here" "Download the build's artifact"
refuses "a tarball step before the download" linux_job_smokes_the_tarball "$m"
mutant smoke-no-verify "$REL" sed '/verify installer 10\.6 \/dev\/null/d'
refuses "a tarball step that never verifies" linux_job_smokes_the_tarball "$m"
mutant smoke-no-fetch "$REL" sed '/fetch installer 10\.6 --into/d'
refuses "a tarball step that never fetches" linux_job_smokes_the_tarball "$m"
mutant smoke-no-help "$REL" sed '/"\$lb" --help/d'
refuses "a tarball step that never runs --help" linux_job_smokes_the_tarball "$m"
exit "$fail"
