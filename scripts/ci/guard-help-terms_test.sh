#!/usr/bin/env bash
#
# Regression test for scripts/ci/guard-help-terms.sh (ut-docs#5243).
# Runs in a sandbox copy (ut-docs#4311): the current tree passes; a planted
# "Führungskraft" in a de topic fails with file:line and "Manager"; the same
# line with `help-term:allow` passes; a planted match in a locale that is not
# listed (en) passes.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox web/help
trap remove_guard_test_sandbox EXIT

GUARD="scripts/ci/guard-help-terms.sh"

fails=0
pass() { echo "ok   - $1"; }
fail() { echo "FAIL - $1" >&2; fails=$((fails + 1)); }
has() { grep -qF -- "$2" <<<"$1"; }

run() { # sets OUT and RC
  RC=0
  OUT="$(bash "${GUARD}" 2>&1)" || RC=$?
}

run
if [ "${RC}" -eq 0 ]; then pass "current tree passes"; else
  fail "current tree should pass"
  echo "${OUT}" >&2
fi

DE="web/help/de/backups.md"
printf 'Bestätigen Sie mit der PIN einer Führungskraft.\n' >>"${DE}"
LINE="$(wc -l <"${DE}" | tr -d ' ')"
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "${DE}:${LINE}:" && has "${OUT}" 'use "Manager"'; then
  pass "planted de term fails with file:line and the replacement"
else
  fail "planted de term: rc=${RC} out=${OUT}"
fi

sed -i '$ s/$/ <!-- help-term:allow self-test -->/' "${DE}"
run
if [ "${RC}" -eq 0 ]; then pass "help-term:allow marker accepts the line"; else
  fail "allow marker: rc=${RC} out=${OUT}"
fi

# Restore de, plant the same term in a locale with no list entry.
sed -i '$ d' "${DE}"
printf 'Confirm with the Führungskraft PIN.\n' >>web/help/en/backups.md
run
if [ "${RC}" -eq 0 ]; then pass "match in a non-listed locale passes"; else
  fail "non-listed locale: rc=${RC} out=${OUT}"
fi

# A broken regex or an unknown locale must fail, never check nothing.
TERMS="scripts/ci/help-terms.txt"
cp "${TERMS}" "${TERMS}.orig"
printf 'de|führungskr(aft|Manager\n' >>"${TERMS}"
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "grep rejected the regex"; then
  pass "bad regex fails"
else
  fail "bad regex: rc=${RC} out=${OUT}"
fi
cp "${TERMS}.orig" "${TERMS}"
printf 'DE|führungskraft|Manager\n' >>"${TERMS}"
run
if [ "${RC}" -ne 0 ] && has "${OUT}" "no help folder web/help/DE"; then
  pass "unknown locale fails"
else
  fail "unknown locale: rc=${RC} out=${OUT}"
fi
cp "${TERMS}.orig" "${TERMS}"

if [ "${fails}" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "✓ guard-help-terms regression test passed"
