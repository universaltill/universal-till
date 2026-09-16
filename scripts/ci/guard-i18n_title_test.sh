#!/usr/bin/env bash
#
# Regression test for guard-i18n.sh's check 10 (ut-docs#2297): proves the
# guard flags a hardcoded English `"title": "..."` literal in
# internal/pages/*.go (the template-data key web/ui/layouts/*.html's
# `<title>{{ .title }}</title>` reads), proves the i18n:ignore escape
# hatch works for a deliberate exception (e.g. the brand name), and proves
# the established httpx.T(...)-based pattern does NOT false-positive.
#
# Separate file from guard-i18n_toast_test.sh (Go-fixture sibling) because
# this check's own fixture set/cleanup is unrelated to ToastMessage.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-i18n.sh"
FIXTURE_DIR="internal/pages"
FAIL_COUNT=0

fixtures=()
cleanup() {
  local status=$?
  if [[ ${#fixtures[@]} -gt 0 ]]; then
    for f in "${fixtures[@]}"; do
      [[ -n "${f}" && -f "${f}" ]] && rm -f "${f}"
    done
  fi
  exit "${status}"
}
trap cleanup EXIT

plant() {
  local name="$1" content="$2"
  local path="${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_title_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_i18n_title_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_i18n_title_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_title_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_i18n_title_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_i18n_title_test_out.$$
}

clear_fixture() {
  local name="$1"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures=()
}

# A hardcoded English "title" literal in a template-data map must be
# rejected -- the exact bug class ut-docs#2297 fixed (48 pages' <title>
# stuck in English regardless of operator locale).
plant "TitleLiteral" 'package pages

func zzGuardTestTitleLiteral() map[string]any {
	return map[string]any{
		"title": "Widgets",
		"theme": "dark",
	}
}'
expect_fail "a hardcoded page <title> literal"
clear_fixture "TitleLiteral"

# Same literal, but marked i18n:ignore on the same line -- the established
# escape hatch (already used by checks 3, 5 and 6), used for real by
# index_page.go's "Universal Till" (a brand name, stays Latin everywhere).
plant "TitleLiteralIgnored" 'package pages

func zzGuardTestTitleLiteralIgnored() map[string]any {
	return map[string]any{
		"title": "Widgets", // i18n:ignore
	}
}'
expect_pass "an i18n:ignore-marked hardcoded title literal"
clear_fixture "TitleLiteralIgnored"

# The real, established pattern: httpx.T(locale, "page.title.<name>") must
# NOT flag -- this is what every page in the codebase now does.
plant "TitleHttpxT" 'package pages

func zzGuardTestTitleHttpxTStub(locale, key string) string { return key }

func zzGuardTestTitleHttpxT(locale string) map[string]any {
	return map[string]any{
		"title": zzGuardTestTitleHttpxTStub(locale, "page.title.widgets"),
	}
}'
expect_pass "a title assigned via httpx.T(...)"
clear_fixture "TitleHttpxT"

# A dynamic title built with fmt.Sprintf(httpx.T(...), ...) -- the pattern
# journal_page.go's receipt-detail title uses -- must NOT flag either: the
# literal on the "title" line is the format-string call, not a bare quote.
plant "TitleSprintfT" 'package pages

import "fmt"

func zzGuardTestTitleSprintfTStub(locale, key string) string { return key }

func zzGuardTestTitleSprintfT(locale, suffix string) map[string]any {
	return map[string]any{
		"title": fmt.Sprintf(zzGuardTestTitleSprintfTStub(locale, "page.title.receipt_detail"), suffix),
	}
}'
expect_pass "a dynamic title built from httpx.T(...) via fmt.Sprintf"
clear_fixture "TitleSprintfT"

# Sanity: the guard must still pass clean on the real, unmodified tree.
expect_pass "the real, unmodified repository tree"

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ ${FAIL_COUNT} guard-i18n_title_test.sh case(s) failed" >&2
  exit 1
fi
echo "✓ guard-i18n_title_test.sh: all cases passed"
