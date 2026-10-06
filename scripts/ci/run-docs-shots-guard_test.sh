#!/usr/bin/env bash
#
# Regression test for run-docs-shots-guard.sh (ut-docs#2794): proves the
# wrapper turns a stale-screenshot failure of guard-docs-shots.sh into a
# `::warning::` on pull_request runs only, and still fails closed on push
# to main and when GITHUB_EVENT_NAME is unset (a developer running it
# locally, or an unknown CI context). The real guard needs a full app build
# plus Playwright to mean anything, so it is replaced with tiny exit-0 /
# exit-1 stubs through the wrapper's GUARD_DOCS_SHOTS_BIN override; the
# guard's own correctness is covered by guard-docs-shots_test.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

WRAPPER="scripts/ci/run-docs-shots-guard.sh"
FAIL_COUNT=0

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "${WORK_DIR}"' EXIT

STUB_PASS="${WORK_DIR}/guard-pass.sh"
STUB_FAIL="${WORK_DIR}/guard-fail.sh"
printf '#!/usr/bin/env bash\necho "stub guard: fresh"\nexit 0\n' >"${STUB_PASS}"
printf '#!/usr/bin/env bash\necho "stub guard: STALE" >&2\nexit 1\n' >"${STUB_FAIL}"
chmod +x "${STUB_PASS}" "${STUB_FAIL}"

# run_wrapper STUB EVENT — EVENT "<unset>" removes GITHUB_EVENT_NAME from the
# environment entirely rather than setting it empty.
run_wrapper() {
  local stub="$1" event="$2"
  if [ "${event}" = "<unset>" ]; then
    env -u GITHUB_EVENT_NAME GUARD_DOCS_SHOTS_BIN="${stub}" bash "${WRAPPER}"
  else
    env GITHUB_EVENT_NAME="${event}" GUARD_DOCS_SHOTS_BIN="${stub}" bash "${WRAPPER}"
  fi
}

expect_pass() {
  local label="$1" stub="$2" event="$3"
  if run_wrapper "${stub}" "${event}" >"${WORK_DIR}/out" 2>&1; then
    echo "✓ wrapper correctly passed ${label}"
  else
    echo "❌ FAIL: expected wrapper to pass ${label}, but it failed" >&2
    cat "${WORK_DIR}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

expect_fail() {
  local label="$1" stub="$2" event="$3"
  if run_wrapper "${stub}" "${event}" >"${WORK_DIR}/out" 2>&1; then
    echo "❌ FAIL: expected wrapper to fail ${label}, but it passed" >&2
    cat "${WORK_DIR}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ wrapper correctly failed ${label}"
  fi
}

# expect_output LABEL PATTERN — asserts on the output of the previous run.
expect_output() {
  local label="$1" pattern="$2"
  if grep -qF -- "${pattern}" "${WORK_DIR}/out"; then
    echo "✓ ${label}"
  else
    echo "❌ FAIL: ${label} — ${pattern@Q} not in output" >&2
    cat "${WORK_DIR}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

expect_no_output() {
  local label="$1" pattern="$2"
  if grep -qF -- "${pattern}" "${WORK_DIR}/out"; then
    echo "❌ FAIL: ${label} — ${pattern@Q} unexpectedly in output" >&2
    cat "${WORK_DIR}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ ${label}"
  fi
}

expect_pass "a fresh guard on pull_request" "${STUB_PASS}" pull_request
expect_no_output "no warning when the guard passes on pull_request" "::warning::"
expect_pass "a fresh guard on push" "${STUB_PASS}" push

expect_pass "a stale guard on pull_request (non-blocking)" "${STUB_FAIL}" pull_request
expect_output "pull_request staleness prints a ::warning:: line" "::warning::guard-docs-shots:"
expect_output "pull_request staleness still shows the real guard's own output" "stub guard: STALE"

expect_fail "a stale guard on push to main" "${STUB_FAIL}" push
expect_output "push staleness shows the real guard's own output" "stub guard: STALE"
expect_no_output "push staleness does not print the non-blocking warning" "::warning::"

expect_fail "a stale guard with GITHUB_EVENT_NAME unset (fail closed)" "${STUB_FAIL}" "<unset>"
expect_fail "a stale guard with GITHUB_EVENT_NAME empty (fail closed)" "${STUB_FAIL}" ""

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} run-docs-shots-guard_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ run-docs-shots-guard_test.sh: all assertions passed"
