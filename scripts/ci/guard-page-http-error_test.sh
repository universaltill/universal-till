#!/usr/bin/env bash
#
# Regression test for guard-page-http-error.sh (ut-docs#1455): proves the
# guard flags a bare http.Error inside an inline page-route closure, proves
# it follows a same-package factory-call handler (the plugins_store_page.go
# shape), proves it does NOT flag an /api or /ui route in the same fixture
# file, proves the "page-error:allow" escape hatch works, and proves the
# guard still passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-page-http-error.sh"
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
  if bash "${GUARD}" >/tmp/guard_page_error_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_page_error_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_page_error_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_page_error_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_page_error_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_page_error_test_out.$$
}

clear_fixture() {
  local name="$1"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures=()
}

# A bare http.Error inside an inline page-route closure must be rejected —
# the exact "failed to load tables" bug class this card fixes.
plant "InlinePageError" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "failed to load", http.StatusInternalServerError)
	})
}'
expect_fail "bare http.Error in an inline page-route closure"
clear_fixture "InlinePageError"

# A same-package factory-call handler (the plugins_store_page.go shape:
# mux.HandleFunc(route, SomeHandler(deps))) must be followed into its
# returned closure, not skipped as a "non-closure handler."
plant "FactoryHandlerError" 'package pages

import "net/http"

func ZzGuardTestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}
}

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz-guard-test", ZzGuardTestHandler())
}'
expect_fail "bare http.Error inside a same-package factory-call handler"
clear_fixture "FactoryHandlerError"

# common.LocalizedError is ALSO a bare, rail-less error body under the
# hood — translating the text doesn't add the layout back. Must be flagged
# exactly like http.Error.
plant "LocalizedErrorFlagged" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
	})
}'
expect_fail "common.LocalizedError in a page-route closure"
clear_fixture "LocalizedErrorFlagged"

# A bare error call inside a LOCAL closure the route handler calls (the
# tables_page.go tiles()/requireManager shape — the exact form the
# reported #1455 incident had) must be followed, not missed.
plant "LocalClosureIndirection" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	requireManager := func(w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	mux.HandleFunc("GET /zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		requireManager(w, r)
	})
}'
expect_fail "bare http.Error reached through a local-closure helper"
clear_fixture "LocalClosureIndirection"

# An /api/ route in the very same file must NOT trip the guard — only page
# routes are in scope; API/htmx-fragment routes keep short bodies.
plant "ApiRouteUntouched" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})
}'
expect_pass "http.Error inside an /api/ route handler"
clear_fixture "ApiRouteUntouched"

# A /ui/ htmx-fragment route must likewise be left alone.
plant "UiRouteUntouched" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui/zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})
}'
expect_pass "http.Error inside a /ui/ fragment route handler"
clear_fixture "UiRouteUntouched"

# A deliberate, explicitly allowlisted exception must be permitted.
plant "AllowlistedException" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable) // page-error:allow zz-guard-test fixture, not a real page
	})
}'
expect_pass "a line carrying an explicit page-error:allow comment"
clear_fixture "AllowlistedException"

# A POST-only route must not be treated as a page a browser navigates to.
plant "PostOnlyUntouched" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("POST /zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})
}'
expect_pass "http.Error inside a POST-only route handler"
clear_fixture "PostOnlyUntouched"

# Baseline: the guard must still pass on the real, unmodified codebase.
if ! bash "${GUARD}" >/tmp/guard_page_error_test_out.$$ 2>&1; then
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced)" >&2
  cat /tmp/guard_page_error_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard still passes on the clean codebase"
fi
rm -f /tmp/guard_page_error_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-page-http-error_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-page-http-error_test.sh: all cases passed"
exit 0
