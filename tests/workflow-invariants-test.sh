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

build_checks_the_digest() {
  job "$1" build | grep -Eq '^[[:space:]]*(- )?run: sh build/shipyard-scripts\.sh check[[:space:]]*$' \
    || { echo "the build job must run sh build/shipyard-scripts.sh check, so a stale digest cannot merge" >&2; return 1; }
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
mutant checkout-creds "$REL" awk '!d && sub(/persist-credentials: false/, "persist-credentials: true") { d = 1 } { print }'
refuses "a release checkout that keeps its token" every_checkout_drops_credentials "$m"
exit "$fail"
