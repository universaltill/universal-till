#!/usr/bin/env bash
#
# Behaviour + wiring test for release-orphan-tag-sweep.sh (ut-docs#5211): a
# release tag that cleanup-orphaned-tag kept for a runner retry that never came
# is deleted (draft release first, then the tag) once the run is old enough --
# and nothing else is: a published release, a pushed tag, a young run, a run
# outside the lookback, a tag already gone or any failed lookup deletes
# nothing. Network functions are redefined with fixtures; nothing touches the
# tree. Delete calls print marker lines that are asserted on.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/release-orphan-tag-sweep.sh"
WORKFLOW="${ROOT_DIR}/.github/workflows/release-orphan-tag-sweep.yml"
RELEASE_YML="${ROOT_DIR}/.github/workflows/release.yml"
CI_YML="${ROOT_DIR}/.github/workflows/ci.yml"

FAIL_COUNT=0
PASS_COUNT=0
fail() { echo "FAIL: $1" >&2; FAIL_COUNT=$((FAIL_COUNT + 1)); }
pass() { echo "ok   - $1"; PASS_COUNT=$((PASS_COUNT + 1)); }

if [ ! -f "${SCRIPT}" ]; then
  echo "FAIL: ${SCRIPT} does not exist" >&2
  exit 1
fi

# shellcheck disable=SC1090
source "${SCRIPT}"
set +e # the sourced scripts set -e; this test reads exit codes itself

export REPO=o/r GH_TOKEN=unused

NOW=1800000000
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }
now_epoch() { echo "${NOW}"; }

# ---- fixtures --------------------------------------------------------------
RUNS_JSON=""
LIST_RUNS_FAILS=0
list_runs() {
  [ "${LIST_RUNS_FAILS}" = 0 ] || return 1
  printf '%s\n' "${RUNS_JSON}"
}

# Per run: JOB_SPEC[run] = "publish-conclusion|cleanup(keep|delete|none)"
declare -A JOB_SPEC TAG_OF
jobs_of_run() {
  local spec="${JOB_SPEC[$1]:-success|none}" pub cleanup
  pub="${spec%%|*}"
  cleanup="${spec##*|}"
  printf '{"jobs":[{"name":"prepare","id":%s1,"conclusion":"success"},{"name":"publish-release","id":%s2,"conclusion":"%s"}' "$1" "$1" "${pub}"
  if [ "${cleanup}" != none ]; then
    printf ',{"name":"cleanup-orphaned-tag","id":%s3,"conclusion":"success"}' "$1"
  fi
  printf ']}\n'
}
annotations_for() {
  local run="${1%3}" spec
  spec="${JOB_SPEC[${run}]:-success|none}"
  case "${spec##*|}" in
    keep)
      printf '%s\n' "Release ${TAG_OF[${run}]} did not publish because a job was never given a runner; keeping the tag and draft release because release-runner-retry.yml will re-run it. If that retry does not happen, release-orphan-tag-sweep.yml deletes the tag and draft after about 2 hours (use \"Re-run failed jobs\" by hand before then)."
      ;;
    delete)
      printf '%s\n' "Release ${TAG_OF[${run}]} did not complete (publish-release: skipped) — deleting the tag so it doesn't burn this version number for the next attempt."
      ;;
  esac
}

declare -A TAG_GONE RELEASES LOOKUP_FAILS DELETE_FAILS TAG_RUN TAG_INFO TAG_LIGHT TAG_LOOKUP_FAILS PUBLISHED_TAG PUBLISHED_ERR
# tag_info <tag>: "sha<TAB>tagger<TAB>message line"; 2 = ref 404, 3 = lightweight
# tag, 1 = any other failure.
tag_info() {
  [ -z "${TAG_GONE[$1]:-}" ] || return 2
  [ -z "${TAG_LOOKUP_FAILS[$1]:-}" ] || return 1
  [ -z "${TAG_LIGHT[$1]:-}" ] || return 3
  if [ -n "${TAG_INFO[$1]:-}" ]; then
    printf '%s\n' "${TAG_INFO[$1]}"
  else
    printf 'sha-%s\tgithub-actions[bot]\tRelease %s\n' "${TAG_RUN[$1]}" "$1"
  fi
}
# published_release <tag>: 0 = a published release exists, 1 = 404, 2 = error.
published_release() {
  [ -z "${PUBLISHED_ERR[$1]:-}" ] || return 2
  [ -z "${PUBLISHED_TAG[$1]:-}" ] || return 0
  return 1
}
releases_for_tag() {
  [ -z "${LOOKUP_FAILS[$1]:-}" ] || return 1
  [ -z "${RELEASES[$1]:-}" ] || printf '%s\n' "${RELEASES[$1]}" | tr ',' '\n'
}
delete_release() {
  if [ -n "${DELETE_FAILS[release-$1]:-}" ]; then return 1; fi
  echo "DEL-RELEASE $1"
}
delete_tag() {
  if [ -n "${DELETE_FAILS[tag-$1]:-}" ]; then return 1; fi
  echo "DEL-TAG $1"
}

reset() {
  RUNS_JSON=""
  LIST_RUNS_FAILS=0
  JOB_SPEC=()
  TAG_OF=()
  TAG_GONE=()
  TAG_RUN=()
  TAG_INFO=()
  TAG_LIGHT=()
  TAG_LOOKUP_FAILS=()
  PUBLISHED_TAG=()
  PUBLISHED_ERR=()
  RELEASES=()
  LOOKUP_FAILS=()
  DELETE_FAILS=()
}

# run_json <id> <event> <status> <conclusion> <created age min> <updated age min>
run_json() {
  printf '{"id":%s,"head_sha":"sha-%s","event":"%s","status":"%s","conclusion":"%s","created_at":"%s","updated_at":"%s"}' \
    "$1" "$1" "$2" "$3" "$4" "$(iso $((NOW - $5 * 60)))" "$(iso $((NOW - $6 * 60)))"
}
# one eligible-by-default run: dispatch, completed, failure, 5h old, idle 4h
good_run() { run_json "$1" workflow_dispatch completed failure 300 240; }
runs_of() { # join args with commas
  local IFS=,
  RUNS_JSON="{\"workflow_runs\":[$*]}"
}
set_keep() { JOB_SPEC[$1]="skipped|keep"; TAG_OF[$1]="$2"; TAG_RUN[$2]="$1"; }

# check <label> <expected stdout markers, newline separated> <expected rc>
check() {
  local label="$1" want="$2" wantrc="$3" out rc got
  rc=0
  out="$(main 2>&1)" || rc=$?
  got="$(grep -E '^DEL-' <<<"${out}")" || true
  LAST_OUT="${out}"
  if [ "${got}" != "${want}" ]; then
    fail "${label}: deletes were [${got}], want [${want}] (output: ${out})"
  elif [ "${rc}" != "${wantrc}" ]; then
    fail "${label}: exit ${rc}, want ${wantrc} (output: ${out})"
  else
    pass "${label}"
  fi
}
LAST_OUT=""

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; RELEASES[v1.0.0]="7	true"
check "kept tag + draft release: draft then tag deleted" $'DEL-RELEASE 7\nDEL-TAG v1.0.0' 0
[[ "${LAST_OUT}" == *"::warning::"*"v1.0.0"*"100"*"dispatch a fresh release"* ]] || fail "warning must name tag, run id and 'dispatch a fresh release': ${LAST_OUT}"

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0
check "kept tag, no release object: tag only" "DEL-TAG v1.0.0" 0

reset; runs_of "$(run_json 100 workflow_dispatch completed failure 300 30)"; set_keep 100 v1.0.0; RELEASES[v1.0.0]="7	true"
check "run updated 30 min ago: nothing" "" 0

reset; runs_of "$(run_json 100 workflow_dispatch completed failure 5000 4000)"; set_keep 100 v1.0.0
check "created before the lookback: nothing" "" 0

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; RELEASES[v1.0.0]="7	false"
check "matching release is published: nothing" "" 0
[[ "${LAST_OUT}" == *"::warning::"* ]] || fail "published release must print a warning: ${LAST_OUT}"

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; RELEASES[v1.0.0]=$'7\ttrue,8\tfalse'
check "one draft and one published release: nothing" "" 0

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; LOOKUP_FAILS[v1.0.0]=1
check "releases lookup fails: nothing" "" 0
[[ "${LAST_OUT}" == *"::warning::"* ]] || fail "failed lookup must print a warning: ${LAST_OUT}"

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; TAG_GONE[v1.0.0]=1; RELEASES[v1.0.0]="7	true"
check "tag already gone: nothing" "" 0

reset; runs_of "$(run_json 100 workflow_dispatch in_progress "" 300 240)"; set_keep 100 v1.0.0
check "run in_progress: nothing" "" 0

reset; runs_of "$(run_json 100 workflow_dispatch completed success 300 240)"; set_keep 100 v1.0.0
check "run succeeded: nothing" "" 0

reset; runs_of "$(run_json 100 push completed failure 300 240)"; set_keep 100 v1.0.0
check "event push (human-pushed tag): nothing" "" 0

reset; runs_of "$(good_run 100)"; JOB_SPEC[100]="success|keep"; TAG_OF[100]=v1.0.0
check "latest attempt published: nothing" "" 0

reset; runs_of "$(good_run 100)"; JOB_SPEC[100]="skipped|delete"; TAG_OF[100]=v1.0.0
check "cleanup annotation is the delete warning: nothing" "" 0

reset; runs_of "$(good_run 100)"; JOB_SPEC[100]="skipped|none"; TAG_OF[100]=v1.0.0
check "no cleanup job: nothing" "" 0

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; RELEASES[v1.0.0]="7	true"; DELETE_FAILS[release-7]=1
check "draft delete fails: tag kept, exit 1" "" 1
[[ "${LAST_OUT}" == *"::error::"* ]] || fail "failed delete must print ::error::: ${LAST_OUT}"

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; DELETE_FAILS[tag-v1.0.0]=1
check "tag delete fails: exit 1" "" 1

reset; LIST_RUNS_FAILS=1
check "list_runs fails: exit 1, nothing deleted" "" 1

# ---- newest-run rule: only the newest Release run is eligible ----------------
reset; runs_of "$(run_json 100 workflow_dispatch completed failure 300 240)" "$(run_json 101 workflow_dispatch in_progress "" 200 5)"
set_keep 100 v1.0.0; TAG_OF[101]=v1.0.0; RELEASES[v1.0.0]="7	true"
check "older kept-tag run + newer in_progress dispatch run, same tag: nothing" "" 0

reset; runs_of "$(run_json 100 workflow_dispatch completed failure 300 240)" "$(run_json 101 push completed success 200 190)"
set_keep 100 v1.0.0; RELEASES[v1.0.0]="7	true"
check "older kept-tag run + newer completed push run: nothing" "" 0
[[ "${LAST_OUT}" == *"a newer Release run exists; skipping."* ]] || fail "skip line missing: ${LAST_OUT}"

reset; runs_of "$(run_json 100 workflow_dispatch completed failure 400 300)" "$(good_run 101)" "$(run_json 102 workflow_dispatch completed failure 500 450)"
set_keep 101 v1.0.1; RELEASES[v1.0.1]="9	true"; set_keep 100 v1.0.0; set_keep 102 v1.0.2
check "newest eligible even when list order is not newest-first" $'DEL-RELEASE 9\nDEL-TAG v1.0.1' 0

# ---- tag identity + published-release defence in depth ----------------------
reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; TAG_INFO[v1.0.0]=$'othersha\tgithub-actions[bot]\tRelease v1.0.0'
check "tag sha is not the run's head_sha: nothing" "" 0
[[ "${LAST_OUT}" == *"::warning::"*"is not the one run 100 created; leaving it alone."* ]] || fail "identity warning missing: ${LAST_OUT}"

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; TAG_LIGHT[v1.0.0]=1
check "lightweight tag: nothing" "" 0
[[ "${LAST_OUT}" == *"is not the one run 100 created; leaving it alone."* ]] || fail "identity warning missing (lightweight): ${LAST_OUT}"

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; TAG_INFO[v1.0.0]=$'sha-100\tSomeone Else\tRelease v1.0.0'
check "tag created by another tagger: nothing" "" 0

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; TAG_INFO[v1.0.0]=$'sha-100\tgithub-actions[bot]\tmy own tag'
check "tag with another message: nothing" "" 0

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; TAG_LOOKUP_FAILS[v1.0.0]=1; RELEASES[v1.0.0]="7	true"
check "tag lookup fails (not 404): nothing" "" 0

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; RELEASES[v1.0.0]="7	true"; PUBLISHED_TAG[v1.0.0]=1
check "releases/tags says published while the listing does not: nothing" "" 0
[[ "${LAST_OUT}" == *"::warning::"* ]] || fail "published release must print a warning: ${LAST_OUT}"

reset; runs_of "$(good_run 100)"; set_keep 100 v1.0.0; RELEASES[v1.0.0]="7	true"; PUBLISHED_ERR[v1.0.0]=1
check "releases/tags errors (not 404): nothing" "" 0

# ---- wiring ------------------------------------------------------------------
wf="" rel="" ci=""
if [ -f "${WORKFLOW}" ]; then wf="$(<"${WORKFLOW}")"; fi
rel="$(<"${RELEASE_YML}")"
ci="$(<"${CI_YML}")"

# has <desc> <haystack> <needle>
has() {
  if [[ "$2" == *"$3"* ]]; then pass "wiring: $1"; else fail "wiring: $1 (missing: $3)"; fi
}

if [ -z "${wf}" ]; then
  fail "wiring: ${WORKFLOW} missing"
else
  has "hourly schedule" "${wf}" $'\n  schedule:\n    - cron: "23 * * * *"'
  has "workflow_dispatch" "${wf}" $'\n  workflow_dispatch:'
  has "top-level permissions {}" "${wf}" $'\npermissions: {}\n'
  perms="$(awk '/^    permissions:/{f=1;next} f&&/^      [a-z-]+:/{sub(/ *#.*/,"");gsub(/^ +/,"");print;next} f{exit}' <<<"${wf}")"
  want_perms=$'contents: write\nactions: read\nchecks: read'
  if [ "${perms}" = "${want_perms}" ]; then
    pass "wiring: job permissions exactly contents write / actions read / checks read"
  else
    fail "wiring: job permissions are [${perms}]"
  fi
  has "timeout-minutes set" "${wf}" "timeout-minutes:"
  has "runs the script" "${wf}" "bash scripts/ci/release-orphan-tag-sweep.sh"
fi
has "release.yml keep phrase pinned" "${rel}" "did not publish because a job was never given a runner; keeping the tag"
has "release.yml names the sweep" "${rel}" "release-orphan-tag-sweep.yml"
has "ci.yml runs this test" "${ci}" "bash scripts/ci/release-orphan-tag-sweep_test.sh"

echo
echo "${PASS_COUNT} passed, ${FAIL_COUNT} failed"
[ "${FAIL_COUNT}" = 0 ]
