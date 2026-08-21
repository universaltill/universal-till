#!/usr/bin/env bash
#
# Regression test for guard-i18n.sh's check 6 (ut-docs#237): proves the
# guard actually flags a hardcoded prose string assigned straight to
# pos.Basket.ToastMessage (both the dotted-field-assignment and the
# composite-literal-key syntax), proves the i18n:ignore escape hatch
# works, proves a literal Sprintf format string on ToastMessage is also
# caught, and proves the existing httpx.T(...)/plain-identifier assignment
# patterns already used throughout pos_api.go/hold_api.go/etc. do NOT
# false-positive. Also proves the guard still passes on the real,
# unmodified codebase.
#
# Separate file from guard-i18n_test.sh (which covers check 5's inline-JS
# fixtures under web/ui/**/*.html) because this check's fixtures are Go
# files under internal/**/*.go — different glob, different directory,
# clearer to keep the two plant/cleanup sets from ever mixing.
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
  if bash "${GUARD}" >/tmp/guard_i18n_toast_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_i18n_toast_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_i18n_toast_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_toast_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_i18n_toast_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_i18n_toast_test_out.$$
}

clear_fixture() {
  local name="$1"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures=()
}

# A hardcoded prose string assigned directly to .ToastMessage must be
# rejected — the exact bug class ut-docs#213 fixed and ut-docs#237 found
# still has no regression gate against a sixth case shipping the same way.
plant "ToastLiteral" 'package pages

func zzGuardTestToastLiteral() {
	var b struct{ ToastMessage string }
	b.ToastMessage = "Applied your discount now"
}'
expect_fail "a hardcoded prose .ToastMessage literal"
clear_fixture "ToastLiteral"

# Same literal, but marked i18n:ignore on the same line — the established
# escape hatch (already used by checks 3 and 5) must exempt it here too.
plant "ToastLiteralIgnored" 'package pages

func zzGuardTestToastLiteralIgnored() {
	var b struct{ ToastMessage string }
	b.ToastMessage = "Applied your discount now" // i18n:ignore
}'
expect_pass "an i18n:ignore-marked hardcoded ToastMessage literal"
clear_fixture "ToastLiteralIgnored"

# A literal Sprintf format string assigned to ToastMessage (not routed
# through httpx.T at all) must also be rejected.
plant "ToastSprintfLiteral" 'package pages

import "fmt"

func zzGuardTestToastSprintfLiteral(code string) {
	var b struct{ ToastMessage string }
	b.ToastMessage = fmt.Sprintf("Item %s not found", code)
}'
expect_fail "a literal Sprintf format string on ToastMessage"
clear_fixture "ToastSprintfLiteral"

# The real, established pattern: assigning the result of httpx.T(...) must
# NOT flag — this is what every httpx.T-based ToastMessage assignment in
# the codebase already does (pos_api.go:349 etc.). Uses a local stub
# instead of importing the real httpx package so this fixture compiles
# standalone (go build/go test/gopls can run against internal/pages while
# this file is transiently planted, same as the other fixtures below).
plant "ToastHttpxT" 'package pages

func zzGuardTestHttpxTStub(locale, key string) string { return key }

func zzGuardTestToastHttpxT(locale string) {
	var b struct{ ToastMessage string }
	b.ToastMessage = zzGuardTestHttpxTStub(locale, "pos.toast.scan_prompt")
}'
expect_pass "a ToastMessage assigned via httpx.T(...)"
clear_fixture "ToastHttpxT"

# A composite-literal assignment (ToastMessage: "...") is the idiomatic Go
# alternative to b.ToastMessage = "..." and is already how this codebase
# builds Basket (internal/pos/service.go's `Basket{}` literals) — the exact
# same bug class in different syntax must be caught too, not just the
# dotted-field-assignment form.
plant "ToastCompositeLiteral" 'package pages

func zzGuardTestToastCompositeLiteral() any {
	type basket struct{ ToastMessage string }
	return basket{ToastMessage: "Applied your discount now"}
}'
expect_fail "a hardcoded prose ToastMessage composite-literal key"
clear_fixture "ToastCompositeLiteral"

# The real, established pattern: assigning a plain identifier must NOT
# flag — this is the msg/toast/message variable this codebase already
# threads through from a caller that itself used httpx.T (pos_api.go:657,
# hold_api.go:136, self_order_shop.go:462, pos_modifiers_api.go:122). A
# known, accepted heuristic gap (same tradeoff check 3 already documents),
# not something this check attempts to trace.
plant "ToastIdentifier" 'package pages

func zzGuardTestToastIdentifier(msg string) {
	var b struct{ ToastMessage string }
	b.ToastMessage = msg
}'
expect_pass "a ToastMessage assigned from a plain identifier"
clear_fixture "ToastIdentifier"

# Sanity: the guard must still pass clean on the real, unmodified tree
# (proves this test file itself, and the fixtures' cleanup, leave no
# residue behind).
expect_pass "the real, unmodified repository tree"

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ ${FAIL_COUNT} guard-i18n_toast_test.sh case(s) failed" >&2
  exit 1
fi
echo "✓ guard-i18n_toast_test.sh: all cases passed"
