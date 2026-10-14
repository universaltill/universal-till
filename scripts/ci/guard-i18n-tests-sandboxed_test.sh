#!/usr/bin/env bash
#
# Regression test for guard-i18n-tests-sandboxed.sh (ut-docs#4258): fails on a
# non-sandboxed guard-i18n_*_test.sh fixture, passes on a sandboxed one and on
# the real scripts/ci. Fixtures live in a mktemp dir; nothing is written to
# the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-i18n-tests-sandboxed.sh"
TMP="$(mktemp -d)"
# Invoked via trap, not a direct call (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf "${TMP}"; }
trap cleanup EXIT
FAIL_COUNT=0

expect_fail() {
  local label="$1" dir="$2"
  if bash "${GUARD}" "${dir}" >"${TMP}/out" 2>&1; then
    echo "FAIL: guard passed but should have failed: ${label}"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -q "guard-i18n_x_test.sh" "${TMP}/out"; then
    echo "FAIL: guard failed without naming the file: ${label}"
    cat "${TMP}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "ok:   ${label}"
  fi
}
expect_pass() {
  local label="$1" dir="$2"
  if bash "${GUARD}" "${dir}" >"${TMP}/out" 2>&1; then
    echo "ok:   ${label}"
  else
    echo "FAIL: guard failed but should have passed: ${label}"
    cat "${TMP}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

mkdir -p "${TMP}/bad" "${TMP}/half" "${TMP}/commented" "${TMP}/good" "${TMP}/empty"
cat >"${TMP}/bad/guard-i18n_x_test.sh" <<'FIX'
#!/usr/bin/env bash
ROOT_DIR=x
cd "${ROOT_DIR}"
FIX
# sources the helper but never enters the sandbox
cat >"${TMP}/half/guard-i18n_x_test.sh" <<'FIX'
#!/usr/bin/env bash
source ./i18n-guard-sandbox.sh
FIX
# sources the helper but the enter call is commented out
cat >"${TMP}/commented/guard-i18n_x_test.sh" <<'FIX'
#!/usr/bin/env bash
source ./i18n-guard-sandbox.sh
# enter_i18n_guard_sandbox
FIX
cat >"${TMP}/good/guard-i18n_x_test.sh" <<'FIX'
#!/usr/bin/env bash
source "$(dirname "$0")/i18n-guard-sandbox.sh"
enter_i18n_guard_sandbox
FIX

expect_fail "a test that never sources the sandbox helper" "${TMP}/bad"
expect_fail "a test that sources the helper but never enters the sandbox" "${TMP}/half"
expect_fail "a test whose enter call is commented out" "${TMP}/commented"
expect_pass "a test that sources the helper and enters the sandbox" "${TMP}/good"
if bash "${GUARD}" "${TMP}/empty" >/dev/null 2>&1; then
  echo "FAIL: guard passed on a dir with no tests"
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "ok:   a dir with no guard-i18n_*_test.sh fails"
fi
expect_pass "the real scripts/ci" "${ROOT_DIR}/scripts/ci"

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "${FAIL_COUNT} case(s) failed"
  exit 1
fi
echo "all cases passed"
