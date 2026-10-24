#!/usr/bin/env bash
#
# Regression test for scripts/ci/guard-help-breadcrumbs.sh (ut-docs#4696).
# Runs in a sandbox copy (ut-docs#4311): the current tree passes; a planted
# translated breadcrumb that differs from the locale's label fails with
# file:line and the expected label; a baseline entry for it makes it pass;
# a stale baseline entry fails.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox web/help web/locales
trap remove_guard_test_sandbox EXIT

GUARD="scripts/ci/guard-help-breadcrumbs.sh"
BASELINE="scripts/ci/i18n-baseline/help-breadcrumb-baseline.txt"
# A language pack outside the sandbox must not change the outcome.
unset UT_DE_PACK_DIR

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }
has() { grep -qF -- "$2" <<<"$1"; }

run() { # run <var> : sets OUT and RC
  RC=0
  OUT="$(bash "${GUARD}" 2>&1)" || RC=$?
}

run
if [ "${RC}" -eq 0 ]; then pass "current tree passes"; else
  fail "current tree should pass"
  echo "${OUT}" >&2
fi

# Plant: tr Updates topic, item 1, uses a different section name.
TOPIC="web/help/tr/updates.md"
sed -i '0,/Ayarlar → Yazılım güncellemesi →/s//Ayarlar → Yazılım güncelleme →/' "${TOPIC}"
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "${TOPIC}:15:" && has "${OUT}" 'expected "Yazılım güncellemesi"'; then
  pass "planted mismatch fails with file:line and the expected label"
else
  fail "planted mismatch: rc=${RC} out=${OUT}"
fi

# Baseline entry makes it pass.
printf 'tr/updates.md:1:settings.update.title planted by the self-test\n' >>"${BASELINE}"
run
if [ "${RC}" -eq 0 ] && has "${OUT}" "known:"; then
  pass "baseline entry accepts the mismatch"
else
  fail "baseline entry: rc=${RC} out=${OUT}"
fi

# Fixing the prose leaves the entry stale.
sed -i '0,/Ayarlar → Yazılım güncelleme →/s//Ayarlar → Yazılım güncellemesi →/' "${TOPIC}"
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "stale baseline entry tr/updates.md:1:settings.update.title"; then
  pass "stale baseline entry fails"
else
  fail "stale entry: rc=${RC} out=${OUT}"
fi

# Garbled baseline line fails.
printf 'nonsense\n' >"${BASELINE}"
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "malformed entry"; then
  pass "malformed baseline line fails"
else
  fail "malformed entry: rc=${RC} out=${OUT}"
fi

if [ "${fails}" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "✓ guard-help-breadcrumbs regression test passed"
