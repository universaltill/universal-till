#!/usr/bin/env bash
#
# Regression test for guard-shellcheck-version.sh (ut-docs#1955): proves
# the guard actually rejects a drifted shellcheck version and a missing
# binary, not just that it happens to pass on whatever shellcheck this
# runner has installed. Uses a disposable fake `shellcheck` binary placed
# earlier on PATH rather than the machine's real one, since the real
# version varies by runner image and would make this test flaky (and
# would defeat the point — this test must exercise drift, not just the
# baseline).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-shellcheck-version.sh"
BASELINE="0.10.0"
FAIL_COUNT=0

FAKE_BIN_DIR="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT`, not a direct call -- shellcheck cannot see that (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() {
  local status=$?
  rm -rf "${FAKE_BIN_DIR}"
  exit "${status}"
}
trap cleanup EXIT

make_fake_shellcheck() {
  local version="$1"
  cat >"${FAKE_BIN_DIR}/shellcheck" <<EOF
#!/usr/bin/env bash
echo "ShellCheck - shell script analysis tool"
echo "version: ${version}"
echo "license: GNU General Public License, version 3"
echo "website: https://www.shellcheck.net"
EOF
  chmod +x "${FAKE_BIN_DIR}/shellcheck"
}

expect_fail() {
  local label="$1"
  local path="$2"
  if PATH="${path}" bash "${GUARD}" >/tmp/guard_shellcheck_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_shellcheck_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_shellcheck_test_out.$$
}

expect_pass() {
  local label="$1"
  local path="$2"
  if PATH="${path}" bash "${GUARD}" >/tmp/guard_shellcheck_test_out.$$ 2>&1; then
    echo "✓ guard correctly accepted ${label}"
  else
    echo "❌ FAIL: expected guard to accept ${label}, but it rejected it" >&2
    cat /tmp/guard_shellcheck_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_shellcheck_test_out.$$
}

# A shellcheck reporting exactly the pinned baseline must pass.
make_fake_shellcheck "${BASELINE}"
expect_pass "a shellcheck reporting the pinned baseline version" "${FAKE_BIN_DIR}:${PATH}"

# A shellcheck reporting anything else — older or newer — must fail loudly,
# not silently pass through.
make_fake_shellcheck "0.11.3"
expect_fail "a shellcheck reporting a drifted (newer) version" "${FAKE_BIN_DIR}:${PATH}"

make_fake_shellcheck "0.9.0"
expect_fail "a shellcheck reporting a drifted (older) version" "${FAKE_BIN_DIR}:${PATH}"

# No shellcheck binary on PATH at all must fail loudly too, rather than the
# script tripping over a missing command with a confusing raw error.
rm -f "${FAKE_BIN_DIR}/shellcheck"
expect_fail "a PATH with no shellcheck binary at all" "${FAKE_BIN_DIR}"

# Output with no "version:" line at all (an unexpected --version format, or
# a broken binary printing nothing useful) must fail WITH the intended
# "could not parse" diagnostic, not just any nonzero exit -- independent
# review (ut-docs#1955) found this exact case previously died silently
# under `set -eo pipefail` (grep's exit 1 propagating through the pipe)
# before that diagnostic ever ran, so `expect_fail`'s bare exit-code check
# alone would not have caught a regression back to that bug.
cat >"${FAKE_BIN_DIR}/shellcheck" <<'EOF'
#!/usr/bin/env bash
echo "not a version string at all"
EOF
chmod +x "${FAKE_BIN_DIR}/shellcheck"
if PATH="${FAKE_BIN_DIR}:${PATH}" bash "${GUARD}" >/tmp/guard_shellcheck_test_out.$$ 2>&1; then
  echo "❌ FAIL: expected guard to reject unparseable --version output, but it passed" >&2
  cat /tmp/guard_shellcheck_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
elif ! grep -q "could not parse a version" /tmp/guard_shellcheck_test_out.$$; then
  echo "❌ FAIL: guard rejected unparseable --version output but without its intended diagnostic (silent failure regression?)" >&2
  cat /tmp/guard_shellcheck_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard correctly rejected unparseable --version output, with its intended diagnostic"
fi
rm -f /tmp/guard_shellcheck_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-shellcheck-version_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-shellcheck-version_test.sh: all cases passed"
exit 0
