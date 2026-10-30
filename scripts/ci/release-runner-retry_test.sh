#!/usr/bin/env bash
#
# Behaviour test for release-runner-retry.sh (ut-docs#5175): proves a macOS
# release job that GitHub never gave a runner (run 40889814547, v0.35.3) is
# re-run, and that nothing else is — a real failure, an unpublished release,
# an exhausted attempt budget and an all-green run never trigger a re-run.
# Network functions are redefined with fixtures; nothing touches the tree.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT_DIR}/scripts/ci/release-runner-retry.sh"

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

STARVED_MSG="The job was not acquired by Runner of type hosted even after multiple attempts"

JOBS_JSON=""
STARVED_IDS=" "
LIST_FAILS=0

list_jobs() {
  if [ "${LIST_FAILS}" = 1 ]; then
    return 1
  fi
  printf '%s\n' "${JOBS_JSON}"
}
annotations_for() {
  if [[ "${STARVED_IDS}" == *" $1 "* ]]; then
    printf '%s\n%s\n' "Due to capacity constraints, jobs targeting macOS arm64 runners may experience longer queue times." "${STARVED_MSG}"
  else
    printf '%s\n' "Process completed with exit code 1."
  fi
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

# run_case <label> <expected reruns> <attempt> -- (uses JOBS_JSON/STARVED_IDS)
run_case() {
  local label="$1" want="$2" attempt="$3" out rc
  rc=0
  out="$(RUN_ATTEMPT="${attempt}" main 2>&1)" || rc=$?
  # main runs in a subshell, so rerun_failed reports through a marker line.
  local got=0
  [[ "${out}" == *"RERUN-CALLED"* ]] && got=1
  if [ "${rc}" -ne 0 ]; then
    fail "${label}: exit ${rc}"
  elif [ "${got}" != "${want}" ]; then
    fail "${label}: reruns=${got}, want ${want} (output: ${out})"
  else
    pass "${label}"
  fi
}
rerun_failed() { echo "RERUN-CALLED"; }

# Read by the sourced script's network functions (redefined above, so unused).
export REPO=o/r RUN_ID=1

# (a) the real incident
mkjobs "prepare:1:success" "publish-release:2:success" "macos-app:3:cancelled" "macos-dmg-attach:4:skipped"
STARVED_IDS=" 3 "
run_case "(a) starved macos-app after published release is re-run" 1 1

# (b) cancelled but not starved (a human cancel)
STARVED_IDS=" "
run_case "(b) cancelled without starvation annotation is not re-run" 0 1

# (c) a real failure next to a starved job
mkjobs "publish-release:2:success" "macos-app:3:cancelled" "windows-installer:5:failure"
STARVED_IDS=" 3 "
run_case "(c) a real failure alongside a starved job is not re-run" 0 1

# (d) release never published
mkjobs "publish-release:2:failure" "macos-app:3:cancelled"
STARVED_IDS=" 3 "
run_case "(d) unpublished release is not re-run" 0 1

# (e) attempts exhausted
mkjobs "publish-release:2:success" "macos-app:3:cancelled" "macos-dmg-attach:4:skipped"
STARVED_IDS=" 3 "
run_case "(e) attempt >= MAX_ATTEMPTS is not re-run" 0 3
MAX_ATTEMPTS=5 run_case "(e2) MAX_ATTEMPTS is overridable" 1 3

# (f) all green
mkjobs "publish-release:2:success" "macos-app:3:success"
STARVED_IDS=" "
run_case "(f) all-success run is not re-run" 0 1

# (g) failing jobs listing fails the script
LIST_FAILS=1
rc=0
RUN_ATTEMPT=1 main >/dev/null 2>&1 || rc=$?
if [ "${rc}" -ne 0 ]; then pass "(g) failing jobs listing exits non-zero"; else fail "(g) failing jobs listing exited 0"; fi
LIST_FAILS=0

echo "${PASS_COUNT} passed, ${FAIL_COUNT} failed"
[ "${FAIL_COUNT}" -eq 0 ]
