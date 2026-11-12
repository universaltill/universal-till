#!/usr/bin/env bash
#
# Regression test for guard-android-i18n.sh (ut-docs#5480, ADR-0160 §2):
# a locale MISSING a base key is a non-blocking warning (English-only code
# PRs); orphan keys, resource-type mismatches and placeholder mismatches stay
# blocking. Runs in a throwaway copy (guard-test-sandbox.sh, #4311).
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox android/app/src/main/res
trap 'remove_guard_test_sandbox' EXIT

RES=android/app/src/main/res
PRISTINE="$(mktemp -d)"
trap 'rm -rf "${PRISTINE}"; remove_guard_test_sandbox' EXIT
cp -r "${RES}" "${PRISTINE}/res"

FAIL_COUNT=0
OUT=""
RC=0

reset_tree() {
  rm -rf "${RES}"
  cp -r "${PRISTINE}/res" "${RES}"
}

run_guard() {
  RC=0
  OUT="$(bash scripts/ci/guard-android-i18n.sh 2>&1)" || RC=$?
}

# expect <case name> <want rc> [grep pattern ...]
expect() {
  local name="$1" want="$2"
  shift 2
  local ok=1 pat
  if [ "${RC}" -ne "${want}" ]; then ok=0; fi
  for pat in "$@"; do
    if ! grep -qE -- "${pat}" <<<"${OUT}"; then ok=0; fi
  done
  if [ "${ok}" -eq 1 ]; then
    echo "ok   - ${name}"
  else
    echo "FAIL - ${name} (rc=${RC}, want ${want}; patterns: $*)"
    printf '%s\n' "${OUT}" | while IFS= read -r l; do echo "       | $l"; done
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# 1. Baseline.
reset_tree
run_guard
expect "baseline passes" 0 "android-i18n guard"
if grep -q "warning" <<<"${OUT}"; then
  echo "FAIL - baseline prints no warning"; FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 2. Locale lacks a base key -> warning, exit 0.
reset_tree
sed -i '/name="error_bar_action"/d' "${RES}/values-fa/strings.xml"
run_guard
expect "locale missing key is a non-blocking warning" 0 \
  "guard-android-i18n: warning \(non-blocking\): .*values-fa/strings.xml is missing 1 key" \
  "error_bar_action"

# 3. New English key only (the normal code PR) -> one warning per locale.
reset_tree
sed -i 's#</resources>#    <string name="brand_new_key">Hello %1$s</string>\n</resources>#' "${RES}/values/strings.xml"
run_guard
expect "base-only new key warns for each locale, exit 0" 0 \
  "values-ar/strings.xml is missing 1 key" \
  "values-fa/strings.xml is missing 1 key" \
  "values-tr/strings.xml is missing 1 key" \
  "3 locale\(s\) incomplete"
if grep -q "keys match" <<<"${OUT}"; then
  echo "FAIL - success line must not claim keys match when warned"; FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# 4. Orphan key stays blocking.
reset_tree
sed -i 's#</resources>#    <string name="stale_key">Old</string>\n</resources>#' "${RES}/values-tr/strings.xml"
run_guard
expect "orphan key blocks" 1 "values-tr/strings.xml has orphan keys" "stale_key"

# 5. string vs plurals type mismatch blocks.
reset_tree
python3 - "${RES}/values-fa/strings.xml" <<'PY'
import re, sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
s = re.sub(r'<string name="app_name">.*?</string>',
           '<plurals name="app_name"><item quantity="other">x</item></plurals>', s)
open(p, "w", encoding="utf-8").write(s)
PY
run_guard
expect "string vs plurals type mismatch blocks" 1 "'app_name' is a plural"

# 6. Placeholder dropped in a plain string blocks.
reset_tree
sed -i 's#\(name="status_running">\)[^<]*#\1no placeholder#' "${RES}/values-fa/strings.xml"
run_guard
expect "string placeholder mismatch blocks" 1 "'status_running' has placeholders"

# 7. Plural quantity placeholder mismatch blocks; differing quantity sets ok.
reset_tree
sed -i 's#</resources>#    <plurals name="item_count"><item quantity="one">%1$d item</item><item quantity="other">%1$d items</item></plurals>\n</resources>#' "${RES}/values/strings.xml"
sed -i 's#</resources>#    <plurals name="item_count"><item quantity="one">%1$d x</item><item quantity="other">%1$d y</item><item quantity="few">%1$d z</item></plurals>\n</resources>#' "${RES}/values-ar/strings.xml"
sed -i 's#</resources>#    <plurals name="item_count"><item quantity="other">%1$d x</item></plurals>\n</resources>#' "${RES}/values-fa/strings.xml"
sed -i 's#</resources>#    <plurals name="item_count"><item quantity="other">%1$d x</item></plurals>\n</resources>#' "${RES}/values-tr/strings.xml"
run_guard
expect "differing plural quantity sets are fine" 0 "android-i18n guard"
sed -i 's#<item quantity="other">%1$d x</item>#<item quantity="other">x</item>#' "${RES}/values-tr/strings.xml"
run_guard
expect "plural quantity placeholder mismatch blocks" 1 "values-tr/strings.xml's 'item_count' quantity=\"other\""

# 8. Missing in fa does not mask a blocking failure in tr.
reset_tree
sed -i '/name="error_bar_action"/d' "${RES}/values-fa/strings.xml"
sed -i 's#\(name="status_running">\)[^<]*#\1no placeholder#' "${RES}/values-tr/strings.xml"
run_guard
expect "warning plus blocking failure exits 1 and reports both" 1 \
  "warning \(non-blocking\): .*values-fa/strings.xml is missing" \
  "values-tr/strings.xml's 'status_running' has placeholders"

# 9. Non-locale qualifier skipped.
reset_tree
mkdir "${RES}/values-night"
printf '<resources>\n</resources>\n' >"${RES}/values-night/strings.xml"
run_guard
expect "values-night skipped" 0 "skipping non-locale qualifier.*values-night"

# 10. No locale dirs -> exit 0.
reset_tree
rm -rf "${RES}/values-ar" "${RES}/values-fa" "${RES}/values-tr"
run_guard
expect "no locale dirs exits 0" 0 "nothing to check"

# 11. Missing base -> exit 1.
reset_tree
rm -f "${RES}/values/strings.xml"
run_guard
expect "missing base exits 1" 1 "strings.xml missing"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "guard-android-i18n_test: ${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "guard-android-i18n_test: all cases passed"
