#!/usr/bin/env bash
#
# Regression test for guard-i18n.sh's check 7 (ut-docs#1461): proves the
# guard actually flags a typo'd string-literal key passed to
# httpx.RenderError, common.LocalizedError, common.LogAndLocalizedError and
# httpx.T (both the package-qualified call and the bare call inside
# internal/httpx itself), proves the paren-aware argument parser correctly
# handles a nested call in an earlier argument (httpx.T's real
# httpx.ResolveLocale(w, r) locale-argument shape) instead of misreading its
# inner comma as the key argument's boundary, proves the i18n:ignore escape
# hatch works, proves a dynamic (non-literal) key argument is silently
# skipped rather than false-flagged, and proves bare T( is only checked
# inside internal/httpx — a same-named local identifier elsewhere must not
# false-positive. Also proves the guard still passes on the real,
# unmodified codebase.
#
# Separate file from guard-i18n_test.sh/guard-i18n_toast_test.sh (checks
# 5/6's fixtures) because this check needs fixtures in TWO directories —
# internal/pages for the package-qualified call shapes, internal/httpx for
# the bare-T-inside-its-own-package shape — so plant() here takes an
# explicit directory instead of using one fixed FIXTURE_DIR.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-i18n.sh"
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
  local dir="$1" name="$2" content="$3"
  local path="${dir}/zz_guard_test_${name}.go"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_keycall_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_i18n_keycall_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_i18n_keycall_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_keycall_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_i18n_keycall_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_i18n_keycall_test_out.$$
}

clear_fixture() {
  local dir="$1" name="$2"
  rm -f "${dir}/zz_guard_test_${name}.go"
  fixtures=()
}

PAGES="internal/pages"
HTTPX="internal/httpx"

# A typo'd literal key passed to httpx.RenderError must be rejected — the
# exact gap #1455's review found: RenderError makes the typo'd key the
# page's entire heading and body on a full-page kiosk error screen.
plant "${PAGES}" "RenderErrorTypo" 'package pages

import (
	"net/http"
	"net/http/httptest"

	"github.com/universaltill/universal-till/internal/httpx"
)

func zzGuardTestRenderErrorTypo() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.serverXXtypo", nil)
}'
expect_fail "a typo'd httpx.RenderError key literal"
clear_fixture "${PAGES}" "RenderErrorTypo"

# Same literal, i18n:ignore on the same line — the established escape hatch
# (already used by checks 3/5/6) must exempt it here too.
plant "${PAGES}" "RenderErrorIgnored" 'package pages

import (
	"net/http"
	"net/http/httptest"

	"github.com/universaltill/universal-till/internal/httpx"
)

func zzGuardTestRenderErrorIgnored() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.serverXXtypo", nil) // i18n:ignore
}'
expect_pass "an i18n:ignore-marked httpx.RenderError key literal"
clear_fixture "${PAGES}" "RenderErrorIgnored"

# A typo'd literal key passed to common.LocalizedError must be rejected.
plant "${PAGES}" "LocalizedErrorTypo" 'package pages

import (
	"net/http"
	"net/http/httptest"

	"github.com/universaltill/universal-till/internal/pages/common"
)

func zzGuardTestLocalizedErrorTypo() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_requiredXXtypo")
}'
expect_fail "a typo'd common.LocalizedError key literal"
clear_fixture "${PAGES}" "LocalizedErrorTypo"

# A typo'd literal key passed to common.LogAndLocalizedError must be
# rejected (key is its 4th argument, not its last — proves the argument
# index, not just "the last one", is what gets checked).
plant "${PAGES}" "LogAndLocalizedErrorTypo" 'package pages

import (
	"net/http"
	"net/http/httptest"

	"github.com/universaltill/universal-till/internal/pages/common"
)

func zzGuardTestLogAndLocalizedErrorTypo() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.serverXXtypo", "test-tag", nil)
}'
expect_fail "a typo'd common.LogAndLocalizedError key literal"
clear_fixture "${PAGES}" "LogAndLocalizedErrorTypo"

# A typo'd literal key passed to httpx.T (package-qualified, the common
# outside-package call shape) must be rejected.
plant "${PAGES}" "HttpxTTypo" 'package pages

import "github.com/universaltill/universal-till/internal/httpx"

func zzGuardTestHttpxTTypo(locale string) string {
	return httpx.T(locale, "pos.toast.scan_promptXXtypo")
}'
expect_fail "a typo'd httpx.T key literal"
clear_fixture "${PAGES}" "HttpxTTypo"

# The real, tricky shape this check exists to parse correctly: httpx.T's
# locale argument is itself a call with a comma inside it
# (httpx.ResolveLocale(w, r)) — a naive split-on-first-comma would misread
# that inner comma as the key argument's own boundary and silently miss the
# typo below. Must still be rejected.
plant "${PAGES}" "HttpxTNestedLocaleTypo" 'package pages

import (
	"net/http"
	"net/http/httptest"

	"github.com/universaltill/universal-till/internal/httpx"
)

func zzGuardTestHttpxTNestedLocaleTypo() string {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	return httpx.T(httpx.ResolveLocale(w, r), "pos.toast.tender_failedXXtypo")
}'
expect_fail "a typo'd httpx.T key literal behind a nested-call locale argument"
clear_fixture "${PAGES}" "HttpxTNestedLocaleTypo"

# A dynamic (non-literal) key argument can't be verified statically and must
# be silently skipped, not false-flagged — same tradeoff every other check
# in this script already documents.
plant "${PAGES}" "HttpxTDynamicKey" 'package pages

import "github.com/universaltill/universal-till/internal/httpx"

func zzGuardTestHttpxTDynamicKey(locale, key string) string {
	return httpx.T(locale, key)
}'
expect_pass "a dynamic (variable) httpx.T key argument"
clear_fixture "${PAGES}" "HttpxTDynamicKey"

# A real, correctly-spelled key must not flag.
plant "${PAGES}" "HttpxTValidKey" 'package pages

import "github.com/universaltill/universal-till/internal/httpx"

func zzGuardTestHttpxTValidKey(locale string) string {
	return httpx.T(locale, "pos.toast.scan_prompt")
}'
expect_pass "a correctly-spelled httpx.T key literal"
clear_fixture "${PAGES}" "HttpxTValidKey"

# Bare T( with 2 args (locale, key) is internal/httpx's own T function
# calling itself. A typo'd literal there must still be caught.
plant "${HTTPX}" "BareTTypo" 'package httpx

func zzGuardTestBareTTypo(locale string) string {
	return T(locale, "notice.dismissXXtypo")
}'
expect_fail "a typo'd 2-arg bare T( key literal inside internal/httpx"
clear_fixture "${HTTPX}" "BareTTypo"

# Bare T( with 1 arg (key) is the OTHER real shape found in production
# (import_page.go, catalog/handlers.go, invoice_page.go): a handler binds
# the locale up front into a closure —
# `T := funcs["T"].(func(string) string)` or
# `T := func(k string) string { return httpx.T(locale, k) }` — then calls
# it as T(key) dozens of times. An earlier version of this check only
# recognised the 2-arg shape, and only inside internal/httpx, which left
# every one of these ~80 real call sites unchecked (independent review,
# ut-docs#1461) — a typo'd literal here must now be caught too, in ANY
# file, not just internal/httpx.
plant "${PAGES}" "BareTClosureTypo" 'package pages

func zzGuardTestBareTClosureTypo(locale string) string {
	T := func(k string) string { return httpxT(locale, k) }
	return T("import.error.invalid_fileXXtypo")
}

func httpxT(locale, key string) string { return key }'
expect_fail "a typo'd 1-arg bare T(key) closure-call key literal outside internal/httpx"
clear_fixture "${PAGES}" "BareTClosureTypo"

# Same 1-arg closure shape with a correctly-spelled key must not flag.
plant "${PAGES}" "BareTClosureValid" 'package pages

func zzGuardTestBareTClosureValid(locale string) string {
	T := func(k string) string { return httpxT(locale, k) }
	return T("import.error.invalid_file")
}

func httpxT(locale, key string) string { return key }'
expect_pass "a correctly-spelled 1-arg bare T(key) closure-call key literal"
clear_fixture "${PAGES}" "BareTClosureValid"

# A bare T( call with neither 1 nor 2 arguments is a shape this check
# doesn't recognise (nothing in the codebase actually does this — this
# fixture is deliberately contrived) and must be silently skipped rather
# than guessed at, same as any other unrecognised/dynamic shape.
plant "${PAGES}" "BareTUnknownShape" 'package pages

func zzGuardTestBareTUnknownShape(a, b, c string) string {
	T := func(x, y, z string) string { return x + y + z }
	return T("en", "extra", "some.bogus.keyXXtypo")
}'
expect_pass "a bare T( call with an unrecognised (3-argument) shape"
clear_fixture "${PAGES}" "BareTUnknownShape"

# Sanity: the guard must still pass clean on the real, unmodified tree
# (proves this test file itself, and the fixtures' cleanup, leave no
# residue behind).
expect_pass "the real, unmodified repository tree"

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ ${FAIL_COUNT} guard-i18n_keycall_test.sh case(s) failed" >&2
  exit 1
fi
echo "✓ guard-i18n_keycall_test.sh: all cases passed"
