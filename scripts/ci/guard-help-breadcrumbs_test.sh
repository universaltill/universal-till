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
enter_guard_test_sandbox web/help web/locales internal/httpx
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

# RTL arrow direction (ut-docs#5644): a '→' in an RTL help topic fails with
# file:line, in every RTL help dir that exists (he has none in the real
# tree, so this also proves the list comes from httpx.IsRTL); '←' passes.
mkdir -p web/help/he
printf '# x\n\nשלב א → ב\n' >web/help/he/arrow-plant.md
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "web/help/he/arrow-plant.md:3:"; then
  pass "right-pointing arrow in an RTL help topic fails with file:line"
else
  fail "RTL arrow plant: rc=${RC} out=${OUT}"
fi
printf '# x\n\nשלב א ← ב\n' >web/help/he/arrow-plant.md
run
if [ "${RC}" -eq 0 ]; then pass "left-pointing arrow in an RTL help topic passes"; else
  fail "RTL left arrow should pass: rc=${RC} out=${OUT}"
fi
rm -r web/help/he
printf '\nplanted → arrow\n' >>web/help/fa/sell.md
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "web/help/fa/sell.md:"; then
  pass "right-pointing arrow in fa help fails"
else
  fail "fa arrow plant: rc=${RC} out=${OUT}"
fi
sed -i '$d' web/help/fa/sell.md
sed -i '$d' web/help/fa/sell.md
# An IsRTL that can no longer be parsed fails closed instead of checking nothing.
sed -i '/^func IsRTL(/,/^}/{/case "/d}' internal/httpx/httpx.go
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "could not read the RTL languages"; then
  pass "unparsable IsRTL list fails closed"
else
  fail "unparsable IsRTL: rc=${RC} out=${OUT}"
fi
git checkout -q -- internal/httpx/httpx.go # the sandbox is a git repo with the copy staged
run
if [ "${RC}" -eq 0 ]; then pass "tree passes again after removing the plants"; else
  fail "tree should pass after removing plants"
  echo "${OUT}" >&2
fi

# Plant: tr Updates topic, item 1, uses a different section name.
TOPIC="web/help/tr/updates.md"
# The line is computed, not pinned: front-matter keys (e.g. `public:`) shift it (ut-docs#5351).
PLANT_LINE="$(awk -v p='Ayarlar → Yazılım güncellemesi →' 'index($0, p) { print NR; exit }' "${TOPIC}")"
sed -i '0,/Ayarlar → Yazılım güncellemesi →/s//Ayarlar → Yazılım güncelleme →/' "${TOPIC}"
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "${TOPIC}:${PLANT_LINE}:" && has "${OUT}" 'expected "Yazılım güncellemesi"'; then
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
