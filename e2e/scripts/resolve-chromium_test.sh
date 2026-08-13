#!/usr/bin/env bash
#
# Regression test for resolve-chromium.sh (ut-docs#622): proves it resolves
# a genuinely launchable pre-installed Chromium, proves it skips a candidate
# that exists but isn't actually a working Chromium (falls through rather
# than trusting the path blindly), and proves it exits 1 with empty stdout
# when nothing resolves — so docs-shots.sh's fallback to `playwright
# install` is reached correctly.
#
# Does NOT separately test the `[ -x "$c" ]` executability guard in
# resolve-chromium.sh: an earlier version of this file tried to, but the
# only externally observable outcome available to a test running the real
# script (the final resolved path) is identical whether that guard skips
# the candidate outright or the candidate is attempted and fails its smoke
# test — so the case asserted nothing the case below doesn't already cover,
# confirmed by deliberately breaking the guard and watching this suite stay
# green. Left out rather than kept as a test that doesn't test its own
# claim (found in independent review, ut-docs#622).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}/.."

RESOLVER="scripts/resolve-chromium.sh"
FAIL_COUNT=0

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

REAL_CHROMIUM="${PLAYWRIGHT_BROWSERS_PATH:-/opt/pw-browsers}/chromium"

# A real Chromium AND an installed playwright-core (smoke-launch.js's own
# dependency, only present after `npm ci` — docs-shots.sh always runs that
# first, but a bare `bash resolve-chromium_test.sh` in a cold checkout
# would not have it yet) must both be present for this suite to mean
# anything. Neither missing is a failure of this test file — the first is
# exactly the "falls through to playwright install" case other machines
# rely on, and the second just means the fixture hasn't been set up — so
# skip rather than false-fail either way.
if [ ! -x "${REAL_CHROMIUM}" ]; then
  echo "skip: no pre-installed Chromium at ${REAL_CHROMIUM} in this environment"
  exit 0
fi
if [ ! -d "node_modules/playwright-core" ]; then
  echo "skip: node_modules/playwright-core not installed (run npm ci first)"
  exit 0
fi

# A file that exists and is executable but is not actually a working
# Chromium (the case that matters most: a stale/corrupt cache entry must
# not be trusted just because the path exists).
FAKE_BINARY="${TMP_DIR}/not-really-chromium"
printf '#!/usr/bin/env bash\nexit 1\n' >"${FAKE_BINARY}"
chmod +x "${FAKE_BINARY}"

fail() {
  echo "❌ FAIL: $1" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
}
ok() {
  echo "✓ $1"
}

# Case 1: PLAYWRIGHT_CHROMIUM_EXECUTABLE points straight at a real, working
# Chromium — must resolve to exactly that path. `|| true` so a genuine
# resolver failure here is reported as a normal test failure below instead
# of aborting the whole suite silently under `set -e` (ut-docs#622 review).
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE="${REAL_CHROMIUM}" PLAYWRIGHT_BROWSERS_PATH= bash "${RESOLVER}" 2>/dev/null || true)"
if [ "${out}" = "${REAL_CHROMIUM}" ]; then
  ok "resolves a real, working PLAYWRIGHT_CHROMIUM_EXECUTABLE"
else
  fail "expected [${REAL_CHROMIUM}], got [${out}]"
fi

# Case 2: PLAYWRIGHT_CHROMIUM_EXECUTABLE is a fake binary that fails the
# smoke test; the real fallback Chromium must still be found afterward.
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE="${FAKE_BINARY}" PLAYWRIGHT_BROWSERS_PATH= \
  UT_DOCS_SHOTS_TEST=1 UT_DOCS_SHOTS_FALLBACK_CHROMIUM="${REAL_CHROMIUM}" bash "${RESOLVER}" 2>/dev/null || true)"
if [ "${out}" = "${REAL_CHROMIUM}" ]; then
  ok "skips a non-launchable candidate and falls through to the next one"
else
  fail "expected fallthrough to [${REAL_CHROMIUM}], got [${out}]"
fi

# Case 3: nothing resolves — every candidate, including the fallback,
# points at something that doesn't exist or doesn't launch. Must exit
# non-zero with empty stdout, so docs-shots.sh's `|| true` capture is empty
# and it correctly falls back to `playwright install`.
set +e
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE="${FAKE_BINARY}" PLAYWRIGHT_BROWSERS_PATH="${TMP_DIR}/does-not-exist" \
  UT_DOCS_SHOTS_TEST=1 UT_DOCS_SHOTS_FALLBACK_CHROMIUM="${TMP_DIR}/also-does-not-exist" bash "${RESOLVER}" 2>/dev/null)"
status=$?
set -e
if [ "${status}" -ne 0 ] && [ -z "${out}" ]; then
  ok "exits non-zero with empty output when nothing resolves"
else
  fail "expected exit!=0 and empty stdout, got status=${status} out=[${out}]"
fi

# UT_DOCS_SHOTS_TEST gate: without it, UT_DOCS_SHOTS_FALLBACK_CHROMIUM must
# be ignored entirely — an accidentally-exported value must never redirect
# a real run (ut-docs#622 review, nit).
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE= PLAYWRIGHT_BROWSERS_PATH= \
  UT_DOCS_SHOTS_FALLBACK_CHROMIUM="${FAKE_BINARY}" bash "${RESOLVER}" 2>/dev/null || true)"
if [ "${out}" = "${REAL_CHROMIUM}" ]; then
  ok "ignores UT_DOCS_SHOTS_FALLBACK_CHROMIUM when UT_DOCS_SHOTS_TEST is unset"
else
  fail "expected the real fallback [${REAL_CHROMIUM}] (override should be inert), got [${out}]"
fi

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "❌ resolve-chromium_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi
echo "✓ resolve-chromium_test.sh: all cases passed"
