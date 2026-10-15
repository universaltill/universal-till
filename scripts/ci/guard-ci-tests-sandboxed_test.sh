#!/usr/bin/env bash
#
# Regression test for guard-ci-tests-sandboxed.sh (ut-docs#4311): fails on a
# *_test.sh that names a real-tree fixture path without entering a sandbox,
# passes on a sandboxed one (either helper), on a read-only path carrying the
# reviewed allow marker, on a test with no real-tree path, and on the real
# scripts/ci. Fixtures live in a mktemp dir; nothing is written to the real
# tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-ci-tests-sandboxed.sh"
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
  elif ! grep -q "guard-x_test.sh" "${TMP}/out"; then
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

case_file() { mkdir -p "${TMP}/$1" && cat >"${TMP}/$1/guard-x_test.sh"; }

case_file bad <<'FIX'
#!/usr/bin/env bash
cd "${ROOT_DIR}"
FIXTURE_DIR="internal/pages"
FIX
case_file badlocal <<'FIX'
#!/usr/bin/env bash
plant() {
  local path="web/ui/pages/zz.html"
}
FIX
case_file badrooted <<'FIX'
#!/usr/bin/env bash
FIXTURE="${ROOT_DIR}/cmd/unitill-desktop/zz.go"
FIX
case_file singlequoted <<'FIX'
#!/usr/bin/env bash
FIXTURE_DIR='internal/pages'
FIX
case_file redirect <<'FIX'
#!/usr/bin/env bash
printf 'package pages\n' > internal/pages/zz.go
FIX
case_file cpwrite <<'FIX'
#!/usr/bin/env bash
cp "${BACKUP}" web/ui/pages/login.html
FIX
case_file mkdirwrite <<'FIX'
#!/usr/bin/env bash
mkdir -p e2e/seed_demo
FIX
case_file arrayassign <<'FIX'
#!/usr/bin/env bash
FIXTURES=("internal/pages/a.go" "internal/pages/b.go")
FIX
case_file otherroot <<'FIX'
#!/usr/bin/env bash
FIXTURE="${REAL_ROOT}/tests/e2e/zz.spec.ts"
FIX
case_file readonlyassign <<'FIX'
#!/usr/bin/env bash
readonly FIXTURE_DIR="plugins/x"
FIX
case_file half <<'FIX'
#!/usr/bin/env bash
source "$(dirname "$0")/guard-test-sandbox.sh"
FIXTURE_DIR="plugins/x"
FIX
case_file commented <<'FIX'
#!/usr/bin/env bash
source "$(dirname "$0")/guard-test-sandbox.sh"
# enter_guard_test_sandbox internal/pages
FIXTURE_DIR="internal/pages"
FIX
case_file good <<'FIX'
#!/usr/bin/env bash
# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox internal/pages
FIXTURE_DIR="internal/pages"
FIX
case_file goodi18n <<'FIX'
#!/usr/bin/env bash
source "$(dirname "$0")/i18n-guard-sandbox.sh"
enter_i18n_guard_sandbox
FIXTURE_DIR="web/ui/pages"
FIX
case_file allowed <<'FIX'
#!/usr/bin/env bash
SPEC="web/ui/pages/login.html"  # guard-sandbox:allow read-only input
FIX
case_file nopath <<'FIX'
#!/usr/bin/env bash
TMP="$(mktemp -d)"
FIXTURE="${TMP}/internal/pages/zz.go"
FIX
mkdir -p "${TMP}/empty"

expect_fail "a test that names a real-tree fixture dir and never sandboxes" "${TMP}/bad"
expect_fail "a local fixture path inside a helper function" "${TMP}/badlocal"
expect_fail "a fixture path spelled \${ROOT_DIR}/cmd/..." "${TMP}/badrooted"
expect_fail "a single-quoted fixture dir" "${TMP}/singlequoted"
expect_fail "a redirect straight into the tree" "${TMP}/redirect"
expect_fail "a cp onto a tracked file" "${TMP}/cpwrite"
expect_fail "a mkdir under e2e/" "${TMP}/mkdirwrite"
expect_fail "an array of fixture paths" "${TMP}/arrayassign"
expect_fail "a path under \${REAL_ROOT}/tests/" "${TMP}/otherroot"
expect_fail "a readonly assignment" "${TMP}/readonlyassign"
expect_fail "a test that sources the helper but never enters the sandbox" "${TMP}/half"
expect_fail "a test whose enter call is commented out" "${TMP}/commented"
expect_pass "a test that sources guard-test-sandbox.sh and enters it" "${TMP}/good"
expect_pass "a test sandboxed by the i18n helper" "${TMP}/goodi18n"
expect_pass "a read-only real path with the reviewed allow marker" "${TMP}/allowed"
expect_pass "a test whose fixture paths are all under its own mktemp dir" "${TMP}/nopath"
if bash "${GUARD}" "${TMP}/empty" >/dev/null 2>&1; then
  echo "FAIL: guard passed on a dir with no tests"
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "ok:   a dir with no *_test.sh fails"
fi
expect_pass "the real scripts/ci" "${ROOT_DIR}/scripts/ci"

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "${FAIL_COUNT} case(s) failed"
  exit 1
fi
echo "all cases passed"
