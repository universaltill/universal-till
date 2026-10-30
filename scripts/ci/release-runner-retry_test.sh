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
ANN_FAILS=0
annotations_for() {
  if [ "${ANN_FAILS}" = 1 ]; then
    return 1
  fi
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
RERUN_FAILS=0
rerun_failed() {
  echo "RERUN-CALLED"
  [ "${RERUN_FAILS}" = 0 ]
}

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

# (d2) a publish-gating job starved (ut-docs#5205: linux-shells on the hosted
# arm pool; verify-versions ran on macos-14 until #5200): publish-release is
# skipped. cleanup-orphaned-tag keeps the tag for exactly one automatic
# retry, so attempt 1 re-runs and attempt 2 does not (a re-run started with
# GITHUB_TOKEN may not fire another workflow_run).
mkjobs "verify-versions:6:cancelled" "macos-app:3:cancelled" "publish-release:2:skipped" "cleanup-orphaned-tag:7:success"
STARVED_IDS=" 3 6 "
run_case "(d2) starved publish-gating job, publish skipped, is re-run on attempt 1" 1 1
run_case "(d2b) ... but not on attempt 2 (PREPUBLISH_MAX_ATTEMPTS)" 0 2
PREPUBLISH_MAX_ATTEMPTS=3 run_case "(d2c) PREPUBLISH_MAX_ATTEMPTS is overridable" 1 2

# (d3) the ut-docs#5205 incident: the arm linux-shells job starved, so
# goreleaser and publish-release were skipped.
mkjobs "prepare:1:success" "linux-shells (ubuntu-22.04-arm, arm64):8:cancelled" "goreleaser:9:skipped" "publish-release:2:skipped" "cleanup-orphaned-tag:7:success"
STARVED_IDS=" 8 "
run_case "(d3) starved arm linux-shells before publish is re-run on attempt 1" 1 1
run_case "(d3b) ... not on attempt 2" 0 2
verdict="$(printf 'a\tcancelled\t1\npublish-release\tskipped\t0\n' | decide)"
if [ "${verdict}" = "PRERUN a" ]; then pass "(d3c) decide prints PRERUN <names>"; else fail "(d3c) decide gave: ${verdict}"; fi

# (d4) a starved job plus a real failure before publish is never re-run
mkjobs "linux-shells (ubuntu-22.04-arm, arm64):8:cancelled" "windows-installer:5:failure" "goreleaser:9:skipped" "publish-release:2:skipped"
STARVED_IDS=" 8 "
run_case "(d4) starved job plus a real failure pre-publish is not re-run" 0 1

# (d5) nothing failed and publish did not run (e.g. whole run cancelled by hand)
mkjobs "prepare:1:success" "publish-release:2:skipped"
STARVED_IDS=" "
run_case "(d5) unpublished run with no failures is not re-run" 0 1

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

# expect_fail <label>: main must exit non-zero for the current fixtures.
expect_fail() {
  local rc=0
  RUN_ATTEMPT=1 main >/dev/null 2>&1 || rc=$?
  if [ "${rc}" -ne 0 ]; then pass "$1"; else fail "$1 (exited 0)"; fi
}
mkjobs "publish-release:2:success" "macos-app:3:cancelled" "macos-dmg-attach:4:skipped"
STARVED_IDS=" 3 "

# (h) a failing annotations call fails the script
ANN_FAILS=1
expect_fail "(h) failing annotations call exits non-zero"
ANN_FAILS=0

# (i) a failing rerun POST fails the script (no green job with no .dmg)
RERUN_FAILS=1
expect_fail "(i) failing rerun-failed-jobs call exits non-zero"
RERUN_FAILS=0

# (j) executed directly (as the workflow does) with a stub `gh` on PATH: the
# main guard runs main and the real network functions issue the POST.
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
printf '%s\n' "${JOBS_JSON}" >"${TMP}/jobs.json"
cat >"${TMP}/gh" <<'GH'
#!/usr/bin/env bash
echo "$*" >>"${GH_LOG}"
case "$*" in
  *"-X POST"*rerun-failed-jobs*) exit 0 ;;
  *"/jobs?"*) cat "${GH_FIXTURES}/jobs.json" ;;
  *check-runs/3/annotations*) echo "The job was not acquired by Runner of type hosted even after multiple attempts" ;;
  *annotations*) echo "Process completed with exit code 1." ;;
  *) echo "unexpected gh call: $*" >&2; exit 2 ;;
esac
GH
chmod +x "${TMP}/gh"
rc=0
PATH="${TMP}:${PATH}" GH_LOG="${TMP}/gh.log" GH_FIXTURES="${TMP}" \
  REPO=o/r RUN_ID=42 RUN_ATTEMPT=1 bash "${SCRIPT}" >/dev/null 2>&1 || rc=$?
if [ "${rc}" -eq 0 ] && grep -q -- "-X POST repos/o/r/actions/runs/42/rerun-failed-jobs" "${TMP}/gh.log" 2>/dev/null; then
  pass "(j) direct execution re-runs through gh"
else
  fail "(j) direct execution: exit ${rc}, gh calls: $(cat "${TMP}/gh.log" 2>/dev/null)"
fi

# (d6) a failing pre-publish re-run names the kept tag that needs deleting
mkjobs "linux-shells (ubuntu-22.04-arm, arm64):8:cancelled" "publish-release:2:skipped"
STARVED_IDS=" 8 "
RERUN_FAILS=1
rc=0
out="$(RUN_ATTEMPT=1 main 2>&1)" || rc=$?
if [ "${rc}" -ne 0 ] && [[ "${out}" == *"::error::"*"Delete them by hand"* ]]; then
  pass "(d6) failing pre-publish rerun errors and names the kept tag"
else
  fail "(d6) rc=${rc}, output: ${out}"
fi
RERUN_FAILS=0

# keep-tag mode (ut-docs#5205): cleanup-orphaned-tag keeps the tag only when
# the same rule would re-run the run. Its own job is in the listing with an
# empty conclusion (it is still running); that must not matter.
keep_case() { # keep_case <label> <want keep|delete> <attempt>
  local label="$1" want="$2" attempt="$3" out rc=0
  out="$(RUN_ATTEMPT="${attempt}" keep_tag 2>/dev/null)" || rc=$?
  if [ "${rc}" -ne 0 ]; then fail "${label}: exit ${rc}"
  elif [ "${out}" != "${want}" ]; then fail "${label}: got '${out}', want '${want}'"
  else pass "${label}"; fi
}
mkjobs "linux-shells (ubuntu-22.04-arm, arm64):8:cancelled" "goreleaser:9:skipped" "publish-release:2:skipped" "cleanup-orphaned-tag:7:"
STARVED_IDS=" 8 "
keep_case "(k1) keep-tag: starved-only on attempt 1 keeps the tag" keep 1
keep_case "(k2) keep-tag: attempt 2 deletes" delete 2
MAX_ATTEMPTS=1 keep_case "(k2b) keep-tag: MAX_ATTEMPTS reached deletes" delete 1
mkjobs "linux-shells (ubuntu-22.04-arm, arm64):8:cancelled" "windows-installer:5:failure" "publish-release:2:skipped" "cleanup-orphaned-tag:7:"
keep_case "(k3) keep-tag: a real failure deletes" delete 1
mkjobs "linux-shells (ubuntu-22.04-arm, arm64):8:cancelled" "publish-release:2:skipped" "cleanup-orphaned-tag:7:"
STARVED_IDS=" "
keep_case "(k4) keep-tag: cancelled but not starved deletes" delete 1
mkjobs "prepare:1:success" "publish-release:2:skipped" "cleanup-orphaned-tag:7:"
keep_case "(k4b) keep-tag: nothing failed deletes" delete 1
mkjobs "macos-app:3:cancelled" "publish-release:2:success" "cleanup-orphaned-tag:7:"
STARVED_IDS=" 3 "
keep_case "(k4c) keep-tag: published release with a starved job deletes" delete 1
rc=0
RUN_ID=42 RUN_ATTEMPT=1 bash "${SCRIPT}" bogus >/dev/null 2>&1 || rc=$?
if [ "${rc}" -eq 2 ]; then pass "(k4d) unknown CLI arg exits 2"; else fail "(k4d) unknown CLI arg exited ${rc}, want 2"; fi
LIST_FAILS=1
keep_case "(k5) keep-tag: failing jobs listing deletes (exit 0)" delete 1
LIST_FAILS=0
mkjobs "linux-shells (ubuntu-22.04-arm, arm64):8:cancelled" "publish-release:2:skipped" "cleanup-orphaned-tag:7:"
STARVED_IDS=" 8 "
ANN_FAILS=1
keep_case "(k6) keep-tag: failing annotations call deletes (exit 0)" delete 1
ANN_FAILS=0

# (k7) executed directly with `keep-tag` through the stub gh.
mkjobs "linux-shells (ubuntu-22.04-arm, arm64):3:cancelled" "goreleaser:9:skipped" "publish-release:2:skipped" "cleanup-orphaned-tag:7:"
printf '%s\n' "${JOBS_JSON}" >"${TMP}/jobs.json"
rc=0
out="$(PATH="${TMP}:${PATH}" GH_LOG="${TMP}/gh.log" GH_FIXTURES="${TMP}" \
  REPO=o/r RUN_ID=42 RUN_ATTEMPT=1 bash "${SCRIPT}" keep-tag 2>/dev/null)" || rc=$?
if [ "${rc}" -eq 0 ] && [ "${out}" = "keep" ]; then
  pass "(k7) direct execution of keep-tag prints keep"
else
  fail "(k7) direct keep-tag: exit ${rc}, stdout '${out}'"
fi
rc=0
out="$(PATH="${TMP}:${PATH}" GH_LOG="${TMP}/gh.log" GH_FIXTURES="${TMP}" \
  REPO=o/r RUN_ID=42 RUN_ATTEMPT=2 bash "${SCRIPT}" keep-tag 2>/dev/null)" || rc=$?
if [ "${rc}" -eq 0 ] && [ "${out}" = "delete" ]; then
  pass "(k8) direct keep-tag on attempt 2 prints delete"
else
  fail "(k8) direct keep-tag attempt 2: exit ${rc}, stdout '${out}'"
fi

# WIRING (ut-docs#5205): release.yml must use the same rule, and the
# downstream jobs must not run against a release that was never created.
WF="${ROOT_DIR}/.github/workflows/release.yml"
# job_block <job>: the text of one top-level job in release.yml.
job_block() {
  awk -v j="  $1:" '$0 == j {on=1; print; next} on && /^  [a-zA-Z0-9_-]+:$/ {exit} on {print}' "${WF}"
}
cleanup="$(job_block cleanup-orphaned-tag | grep -vE '^[[:space:]]*#' || true)"
if [ -z "${cleanup}" ]; then
  fail "wiring: could not extract cleanup-orphaned-tag from release.yml"
else
  keep_line="$(grep -n 'release-runner-retry.sh keep-tag' <<<"${cleanup}" | head -1 | cut -d: -f1 || true)"
  del_line="$(grep -n 'gh release delete' <<<"${cleanup}" | head -1 | cut -d: -f1 || true)"
  if ! grep -qF '= keep ]' <<<"${cleanup}"; then
    fail "wiring: cleanup-orphaned-tag must compare keep-tag's output with = keep ]"
  elif [ -n "${keep_line}" ] && [ -n "${del_line}" ] && [ "${keep_line}" -lt "${del_line}" ]; then
    pass "wiring: cleanup-orphaned-tag calls keep-tag before it deletes"
  else
    fail "wiring: cleanup-orphaned-tag must call release-runner-retry.sh keep-tag before gh release delete"
  fi
  for perm in "actions: read" "checks: read" "contents: write"; do
    if grep -qE "^[[:space:]]+${perm}([[:space:]]|\$)" <<<"${cleanup}"; then
      pass "wiring: cleanup-orphaned-tag has ${perm}"
    else
      fail "wiring: cleanup-orphaned-tag lacks ${perm}"
    fi
  done
  for envv in REPO RUN_ID RUN_ATTEMPT GH_TOKEN; do
    if grep -qE "^[[:space:]]+${envv}:" <<<"${cleanup}"; then
      pass "wiring: cleanup-orphaned-tag passes ${envv}"
    else
      fail "wiring: cleanup-orphaned-tag lacks env ${envv}"
    fi
  done
fi
for job in windows-installer macos-app android-app verify-versions; do
  blk="$(job_block "${job}")"
  ifl="$(grep -E '^    if:' <<<"${blk}" | head -1 || true)"
  if [ -z "${ifl}" ]; then
    fail "wiring: ${job} has no if: line (parser broke?)"
  elif [[ "${ifl}" == *"needs.goreleaser.result != 'skipped'"* ]]; then
    pass "wiring: ${job} skips when goreleaser was skipped"
  else
    fail "wiring: ${job}'s if: lacks needs.goreleaser.result != 'skipped'"
  fi
done
# The premise: linux-shells (hosted arm pool) still gates goreleaser.
gr_needs="$(job_block goreleaser | grep -E '^    needs:' | head -1 || true)"
if [[ "${gr_needs}" == *linux-shells* ]] && job_block linux-shells | grep -q 'ubuntu-22.04-arm'; then
  pass "wiring premise: goreleaser needs linux-shells, which runs on ubuntu-22.04-arm"
else
  fail "wiring premise changed: goreleaser no longer needs an arm linux-shells job; revisit ut-docs#5205"
fi

echo "${PASS_COUNT} passed, ${FAIL_COUNT} failed"
[ "${FAIL_COUNT}" -eq 0 ]
