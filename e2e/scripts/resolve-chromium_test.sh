#!/usr/bin/env bash
#
# Regression test for resolve-chromium.sh (ut-docs#622, ut-docs#632): proves
# it resolves a genuinely launchable pre-installed Chromium, proves it skips
# a candidate that exists but isn't actually a working Chromium (falls
# through rather than trusting the path blindly), proves it prefers the
# headless-shell variant over full Chrome (ut-docs#632) with a working
# fallback when headless-shell isn't available, and proves it exits 1 with
# empty stdout when nothing resolves — so docs-shots.sh's fallback to
# `playwright install` is reached correctly.
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

BROWSERS_ROOT="${PLAYWRIGHT_BROWSERS_PATH:-/opt/pw-browsers}"
REAL_CHROMIUM="${BROWSERS_ROOT}/chromium"

# Mirrors resolve-chromium.sh's own find_headless_shell: the headless-shell
# variant has no stable convenience symlink (unlike "chromium"), so its path
# is revision-globbed rather than fixed.
find_real_headless_shell() {
  local match
  for match in "${BROWSERS_ROOT}"/chromium_headless_shell-*/chrome-linux/headless_shell; do
    [ -e "${match}" ] && { echo "${match}"; return 0; }
  done
  return 1
}
REAL_HEADLESS_SHELL="$(find_real_headless_shell || true)"

# A real Chromium, a real headless-shell, AND an installed playwright-core
# (smoke-launch.js's own dependency, only present after `npm ci` —
# docs-shots.sh always runs that first, but a bare `bash
# resolve-chromium_test.sh` in a cold checkout would not have it yet) must
# all be present for this suite to mean anything. None missing is a failure
# of this test file — the first two are exactly the "falls through to
# playwright install" / "no variant preference to prove" cases other
# machines rely on, and the third just means the fixture hasn't been set up
# — so skip rather than false-fail either way.
if [ ! -x "${REAL_CHROMIUM}" ]; then
  echo "skip: no pre-installed Chromium at ${REAL_CHROMIUM} in this environment"
  exit 0
fi
if [ -z "${REAL_HEADLESS_SHELL}" ] || [ ! -x "${REAL_HEADLESS_SHELL}" ]; then
  echo "skip: no pre-installed chromium-headless-shell under ${BROWSERS_ROOT} in this environment"
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

# A path that deliberately never exists, used to suppress a variant's
# fallback candidate under the UT_DOCS_SHOTS_TEST gate.
NO_HEADLESS_SHELL="${TMP_DIR}/no-headless-shell-here"
NO_CHROMIUM="${TMP_DIR}/no-chromium-here"

fail() {
  echo "❌ FAIL: $1" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
}
ok() {
  echo "✓ $1"
}

# Case 1: PLAYWRIGHT_CHROMIUM_EXECUTABLE points straight at a real, working
# Chromium — must resolve to exactly that path, bypassing any variant
# preference (ut-docs#632 AC3: the explicit override has absolute
# precedence). `|| true` so a genuine resolver failure here is reported as a
# normal test failure below instead of aborting the whole suite silently
# under `set -e` (ut-docs#622 review).
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE="${REAL_CHROMIUM}" PLAYWRIGHT_BROWSERS_PATH= bash "${RESOLVER}" 2>/dev/null || true)"
if [ "${out}" = "${REAL_CHROMIUM}" ]; then
  ok "resolves a real, working PLAYWRIGHT_CHROMIUM_EXECUTABLE, bypassing variant preference"
else
  fail "expected [${REAL_CHROMIUM}], got [${out}]"
fi

# Case 2 (ut-docs#632 AC1): PLAYWRIGHT_CHROMIUM_EXECUTABLE is a fake binary
# that fails the smoke test; the preferred headless-shell variant must be
# found next — a normal fallback headless launch uses headless-shell, not
# full Chrome, so that's what a reused browser should match first.
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE="${FAKE_BINARY}" PLAYWRIGHT_BROWSERS_PATH= \
  bash "${RESOLVER}" 2>/dev/null || true)"
if [ "${out}" = "${REAL_HEADLESS_SHELL}" ]; then
  ok "skips a non-launchable override and prefers the headless-shell variant"
else
  fail "expected [${REAL_HEADLESS_SHELL}], got [${out}]"
fi

# Case 3 (ut-docs#632 AC2): headless-shell explicitly suppressed (absent in
# this scenario) — must fall back to the full Chrome build, exactly as
# before this variant-preference change existed.
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE="${FAKE_BINARY}" PLAYWRIGHT_BROWSERS_PATH= \
  UT_DOCS_SHOTS_TEST=1 UT_DOCS_SHOTS_FALLBACK_CHROMIUM_HEADLESS_SHELL="${NO_HEADLESS_SHELL}" \
  UT_DOCS_SHOTS_FALLBACK_CHROMIUM="${REAL_CHROMIUM}" bash "${RESOLVER}" 2>/dev/null || true)"
if [ "${out}" = "${REAL_CHROMIUM}" ]; then
  ok "falls back to full Chrome when no headless-shell candidate resolves"
else
  fail "expected fallback to [${REAL_CHROMIUM}], got [${out}]"
fi

# Case 4: nothing resolves — every candidate for every variant, including
# both fallbacks, points at something that doesn't exist or doesn't launch.
# Must exit non-zero with empty stdout, so docs-shots.sh's `|| true` capture
# is empty and it correctly falls back to `playwright install`.
set +e
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE="${FAKE_BINARY}" PLAYWRIGHT_BROWSERS_PATH="${TMP_DIR}/does-not-exist" \
  UT_DOCS_SHOTS_TEST=1 UT_DOCS_SHOTS_FALLBACK_CHROMIUM_HEADLESS_SHELL="${NO_HEADLESS_SHELL}" \
  UT_DOCS_SHOTS_FALLBACK_CHROMIUM="${NO_CHROMIUM}" bash "${RESOLVER}" 2>/dev/null)"
status=$?
set -e
if [ "${status}" -ne 0 ] && [ -z "${out}" ]; then
  ok "exits non-zero with empty output when nothing resolves"
else
  fail "expected exit!=0 and empty stdout, got status=${status} out=[${out}]"
fi

# UT_DOCS_SHOTS_TEST gate: without it, neither UT_DOCS_SHOTS_FALLBACK_CHROMIUM
# nor UT_DOCS_SHOTS_FALLBACK_CHROMIUM_HEADLESS_SHELL must have any effect —
# an accidentally-exported value must never redirect a real run (ut-docs#622
# review, nit). Ungated, the real headless-shell default is what resolves,
# since it's preferred and nothing suppresses it here.
out="$(PLAYWRIGHT_CHROMIUM_EXECUTABLE= PLAYWRIGHT_BROWSERS_PATH= \
  UT_DOCS_SHOTS_FALLBACK_CHROMIUM_HEADLESS_SHELL="${FAKE_BINARY}" \
  UT_DOCS_SHOTS_FALLBACK_CHROMIUM="${FAKE_BINARY}" bash "${RESOLVER}" 2>/dev/null || true)"
if [ "${out}" = "${REAL_HEADLESS_SHELL}" ]; then
  ok "ignores both FALLBACK overrides when UT_DOCS_SHOTS_TEST is unset"
else
  fail "expected the real headless-shell default [${REAL_HEADLESS_SHELL}] (overrides should be inert), got [${out}]"
fi

# Case 6 (ut-docs#632 AC4): expected-chromium-version.js compares against
# the browsers.json entry matching the resolved variant, not always
# "chromium" — proves the script accepts and honors the entry-name arg
# rather than silently ignoring it and always reading "chromium".
#
# Asserting both known entries print a non-empty version would NOT catch a
# regression that silently drops the argv[2] plumbing (hardcodes "chromium"
# regardless of the arg): playwright-core's browsers.json currently pins the
# identical browserVersion for both "chromium" and "chromium-headless-shell"
# in this environment, so an implementation that ignores its arg entirely
# would still pass that assertion (found in independent review, ut-docs#632).
# Instead, prove the arg is genuinely used to select the entry: a bogus
# entry name that doesn't exist in browsers.json must fail — an
# implementation that ignores argv[2] would keep resolving "chromium"
# regardless and succeed anyway.
chromium_version="$(node scripts/expected-chromium-version.js chromium 2>/dev/null || true)"
hs_version="$(node scripts/expected-chromium-version.js chromium-headless-shell 2>/dev/null || true)"
if [ -n "${chromium_version}" ] && [ -n "${hs_version}" ]; then
  ok "expected-chromium-version.js reports a version for both the chromium and chromium-headless-shell entries"
else
  fail "expected non-empty versions for both entries, got chromium=[${chromium_version}] chromium-headless-shell=[${hs_version}]"
fi

if bogus_out="$(node scripts/expected-chromium-version.js this-entry-does-not-exist-632 2>/dev/null)"; then
  fail "expected a bogus browsers.json entry name to fail, but it printed [${bogus_out}] (argv[2] may be silently ignored)"
else
  ok "expected-chromium-version.js genuinely uses argv[2] to select the entry (a bogus name fails, not silently defaults to chromium)"
fi

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "❌ resolve-chromium_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi
echo "✓ resolve-chromium_test.sh: all cases passed"
