#!/usr/bin/env bash
#
# Behaviour test for ci-runner-retry.sh (ut-docs#5490): proves an ios-ci /
# desktop-darwin run whose macOS job GitHub never gave a runner is re-run, and
# that nothing else is — a real failure, a fork PR's run, an exhausted attempt
# budget and an all-green run never trigger a re-run. Network functions are
# redefined with fixtures; nothing touches the tree.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/ci-runner-retry.sh"
WF_DIR="${ROOT_DIR}/.github/workflows"

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
# The script sets -euo pipefail; keep counting FAILs instead of aborting.
set +e

STARVED_MSG="The job was not acquired by Runner of type hosted even after multiple attempts"
JOBS_JSON=""
STARVED_IDS=" "
LIST_FAILS=0
ANN_FAILS=0
RERUN_FAILS=0

list_jobs() {
  [ "${LIST_FAILS}" = 0 ] || return 1
  printf '%s\n' "${JOBS_JSON}"
}
annotations_for() {
  [ "${ANN_FAILS}" = 0 ] || return 1
  if [[ "${STARVED_IDS}" == *" $1 "* ]]; then
    printf '%s\n%s\n' "Due to capacity constraints, jobs targeting macOS arm64 runners may experience longer queue times." "${STARVED_MSG}"
  else
    printf '%s\n' "Process completed with exit code 1."
  fi
}
rerun_failed() {
  echo "RERUN-CALLED"
  [ "${RERUN_FAILS}" = 0 ]
}
# jobs "name:id:conclusion" ...
mkjobs() {
  local first=1 spec n i c
  JOBS_JSON='{"total_count":0,"jobs":['
  for spec in "$@"; do
    IFS=: read -r n i c <<<"${spec}"
    [ "${first}" = 1 ] || JOBS_JSON+=","
    first=0
    JOBS_JSON+="{\"name\":\"${n}\",\"id\":${i},\"conclusion\":\"${c}\"}"
  done
  JOBS_JSON+=']}'
}

export REPO=o/r RUN_ID=1 HEAD_REPO=o/r

# run_case <label> <want reruns 0|1> <attempt> [output needle]
run_case() {
  local label="$1" want="$2" attempt="$3" needle="${4:-}" out rc=0 got=0
  out="$(RUN_ATTEMPT="${attempt}" main 2>&1)" || rc=$?
  [[ "${out}" == *"RERUN-CALLED"* ]] && got=1
  if [ "${rc}" -ne 0 ]; then fail "${label}: exit ${rc} (${out})"
  elif [ "${got}" != "${want}" ]; then fail "${label}: reruns=${got}, want ${want} (${out})"
  elif [ -n "${needle}" ] && [[ "${out}" != *"${needle}"* ]]; then fail "${label}: output lacks '${needle}' (${out})"
  else pass "${label}"; fi
}
expect_fail() { # expect_fail <label>: main exits non-zero
  local rc=0
  RUN_ATTEMPT=1 main >/dev/null 2>&1 || rc=$?
  if [ "${rc}" -ne 0 ]; then pass "$1"; else fail "$1 (exited 0)"; fi
}

# (a) the real incident: ios-ci simulator never got a macOS runner
mkjobs "simulator:3:cancelled" "lint:4:success"
STARVED_IDS=" 3 "
run_case "(a) starved macOS job is re-run" 1 1 "re-running runner-starved jobs: simulator"

# (b) cancelled by a human: no starvation annotation
STARVED_IDS=" "
run_case "(b) cancelled without starvation annotation is not re-run" 0 1 "::warning::"

# (c) a real failure next to a starved job
mkjobs "simulator:3:cancelled" "lint:4:failure"
STARVED_IDS=" 3 "
run_case "(c) a real failure alongside a starved job is not re-run" 0 1 "lint"

# (c2) timed_out is never starvation
mkjobs "simulator:3:timed_out"
STARVED_IDS=" 3 "
run_case "(c2) a timed_out job is not re-run even with the annotation" 0 1 "::warning::"

# (d) attempts exhausted, and the bound is overridable
mkjobs "simulator:3:cancelled"
STARVED_IDS=" 3 "
run_case "(d) attempt >= MAX_ATTEMPTS is not re-run" 0 3 "::warning::"
MAX_ATTEMPTS=5 run_case "(d2) MAX_ATTEMPTS is overridable" 1 3
run_case "(d3) attempt 2 of the default 3 still re-runs" 1 2

# (e) all green / nothing failed
mkjobs "simulator:3:success" "lint:4:skipped"
STARVED_IDS=" "
run_case "(e) all-success run is not re-run" 0 1

# (f) fork PR: workflow_run holds actions:write in the base repo
mkjobs "simulator:3:cancelled"
STARVED_IDS=" 3 "
HEAD_REPO=evil/r run_case "(f) a fork PR's run is skipped with a notice" 0 1 "::notice::"
HEAD_REPO='' run_case "(f2) an unknown head repository is skipped" 0 1 "::notice::"
out="$(unset HEAD_REPO; RUN_ATTEMPT=1 main 2>&1)" || true
if [[ "${out}" == *"::notice::"* && "${out}" != *"RERUN-CALLED"* ]]; then
  pass "(f3) an unset head repository is skipped"
else
  fail "(f3) unset HEAD_REPO: ${out}"
fi

# (g) API failures fail the script (never a silent green)
LIST_FAILS=1
expect_fail "(g) failing jobs listing exits non-zero"
LIST_FAILS=0
ANN_FAILS=1
expect_fail "(g2) failing annotations call exits non-zero"
ANN_FAILS=0
RERUN_FAILS=1
expect_fail "(g3) failing rerun-failed-jobs call exits non-zero"
RERUN_FAILS=0

# (h) decide is pure: verdict lines
v="$(printf 'a\tcancelled\t1\nb\tsuccess\t0\n' | decide_ci)"
if [ "${v}" = "RERUN a" ]; then pass "(h) decide_ci RERUN <names>"; else fail "(h) decide_ci gave: ${v}"; fi
v="$(printf 'a\tcancelled\t1\nb\tfailure\t0\n' | decide_ci)"
if [ "${v}" = "REAL b" ]; then pass "(h2) decide_ci REAL <names>"; else fail "(h2) decide_ci gave: ${v}"; fi
v="$(printf 'a\tsuccess\t0\nb\tskipped\t0\n' | decide_ci)"
if [ "${v}" = "NONE" ]; then pass "(h3) decide_ci NONE"; else fail "(h3) decide_ci gave: ${v}"; fi
v="$(printf 'a\tcancelled\t1\nb\tcancelled\t0\n' | decide_ci)"
if [ "${v}" = "REAL b" ]; then pass "(h4) decide_ci: unstarved cancel is REAL"; else fail "(h4) decide_ci gave: ${v}"; fi
# (h5, h6) only a *cancelled* starved job counts: timed_out/failure stay REAL
# even if a starvation annotation was somehow read for them.
v="$(printf 'a\ttimed_out\t1\n' | decide_ci)"
if [ "${v}" = "REAL a" ]; then pass "(h5) decide_ci: timed_out is REAL even when starved"; else fail "(h5) decide_ci gave: ${v}"; fi
v="$(printf 'a\tfailure\t1\n' | decide_ci)"
if [ "${v}" = "REAL a" ]; then pass "(h6) decide_ci: failure is REAL even when starved"; else fail "(h6) decide_ci gave: ${v}"; fi

# (i) executed directly (as the workflow does) with a stub gh on PATH.
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
mkjobs "simulator:3:cancelled"
printf '%s\n' "${JOBS_JSON}" >"${TMP}/jobs.json"
cat >"${TMP}/gh" <<'GH'
#!/usr/bin/env bash
echo "$*" >>"${GH_LOG}"
case "$*" in
  *"-X POST"*rerun-failed-jobs*) exit 0 ;;
  *"/jobs?"*) cat "${GH_FIXTURES}/jobs.json" ;;
  *check-runs/3/annotations*) echo "The job was not acquired by Runner of type hosted even after multiple attempts" ;;
  *) echo "unexpected gh call: $*" >&2; exit 2 ;;
esac
GH
chmod +x "${TMP}/gh"
rc=0
PATH="${TMP}:${PATH}" GH_LOG="${TMP}/gh.log" GH_FIXTURES="${TMP}" \
  REPO=o/r RUN_ID=42 RUN_ATTEMPT=1 HEAD_REPO=o/r bash "${SCRIPT}" >/dev/null 2>&1 || rc=$?
if [ "${rc}" -eq 0 ] && grep -q -- "-X POST repos/o/r/actions/runs/42/rerun-failed-jobs" "${TMP}/gh.log" 2>/dev/null; then
  pass "(i) direct execution re-runs through gh"
else
  fail "(i) direct execution: exit ${rc}, gh calls: $(cat "${TMP}/gh.log" 2>/dev/null)"
fi
: >"${TMP}/gh.log"
rc=0
PATH="${TMP}:${PATH}" GH_LOG="${TMP}/gh.log" GH_FIXTURES="${TMP}" \
  REPO=o/r RUN_ID=42 RUN_ATTEMPT=1 HEAD_REPO=evil/r bash "${SCRIPT}" >/dev/null 2>&1 || rc=$?
if [ "${rc}" -eq 0 ] && [ ! -s "${TMP}/gh.log" ]; then
  pass "(i2) direct execution for a fork run makes no gh call"
else
  fail "(i2) fork run: exit ${rc}, gh calls: $(cat "${TMP}/gh.log" 2>/dev/null)"
fi

# WIRING: the workflow covers exactly the two macOS workflows, by their name:.
WF="${WF_DIR}/ci-runner-retry.yml"
if [ ! -f "${WF}" ]; then
  fail "wiring: ${WF} does not exist"
else
  want="desktop-darwin ios-ci"
  got="$(awk '/^[[:space:]]+workflows:/ {gsub(/.*\[|\].*/, ""); gsub(/,/, " "); print}' "${WF}" | tr -s ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//')"
  if [ "${got}" = "${want}" ]; then pass "wiring: workflow_run lists exactly ios-ci and desktop-darwin"
  else fail "wiring: workflow_run lists '${got}', want '${want}'"; fi
  for n in ios-ci desktop-darwin; do
    if grep -qxF "name: ${n}" "${WF_DIR}/${n}.yml"; then pass "wiring: ${n}.yml is named ${n}"
    else fail "wiring: ${n}.yml no longer has 'name: ${n}' (the retry matches on it)"; fi
    if grep -qF "ci-runner-retry.yml" "${WF_DIR}/${n}.yml"; then pass "wiring: ${n}.yml header points at ci-runner-retry.yml"
    else fail "wiring: ${n}.yml header does not mention ci-runner-retry.yml"; fi
  done
  nocomment="$(grep -vE '^[[:space:]]*#' "${WF}" || true)"
  if grep -qE '^permissions: \{\}' <<<"${nocomment}"; then pass "wiring: top-level permissions: {}"
  else fail "wiring: top-level permissions: {} missing"; fi
  for perm in "contents: read" "actions: write" "checks: read"; do
    if grep -qE "^[[:space:]]+${perm}([[:space:]]|\$)" <<<"${nocomment}"; then pass "wiring: job has ${perm}"
    else fail "wiring: job lacks ${perm}"; fi
  done
  if grep -qF 'github.event.workflow_run.head_repository.full_name' <<<"${nocomment}" && grep -qE '^[[:space:]]+HEAD_REPO:' <<<"${nocomment}"; then
    pass "wiring: HEAD_REPO comes from the triggering run's head_repository"
  else fail "wiring: HEAD_REPO is not passed from head_repository.full_name"; fi
  # Never run the triggering run's code: no checkout of its ref/sha.
  if grep -qE 'workflow_run\.(head_sha|head_branch)|^[[:space:]]+ref:' <<<"${nocomment}"; then
    fail "wiring: the workflow references the triggering run's ref/sha (never check it out)"
  else pass "wiring: never checks out the triggering run's ref"; fi
  if grep -qE "conclusion != 'success'" <<<"${nocomment}" && grep -qE "conclusion != 'skipped'" <<<"${nocomment}"; then
    pass "wiring: job runs only for a non-success, non-skipped conclusion"
  else fail "wiring: job if: lacks the conclusion filter"; fi
fi

echo "${PASS_COUNT} passed, ${FAIL_COUNT} failed"
[ "${FAIL_COUNT}" -eq 0 ]
