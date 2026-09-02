#!/usr/bin/env bash
#
# Regression test for guard-page-http-error.sh (ut-docs#1455): proves the
# guard actually flags a bare http.Error(...) planted inside a page-route
# handler, proves the "// page-error:allow" escape hatch works, proves a
# POST-only / /api/ route is correctly left alone, and proves the guard
# still passes on the real, unmodified codebase (same shape as
# guard-kiosk-engine_test.sh).
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
  if bash "${GUARD}" >/tmp/guard_page_http_error_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_page_http_error_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_page_http_error_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_page_http_error_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_page_http_error_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_page_http_error_test_out.$$
}

clear_fixture() {
  local name="$1"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures=()
}

# A bare http.Error(...) reachable directly inside a GET page-route
# handler must be rejected — the exact bug class ut-docs#1455 fixed.
plant "DirectHTTPError" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
}'
expect_fail "http.Error reached from a GET page-route handler"
clear_fixture "DirectHTTPError"

# A bare-path (no method prefix) page route must be caught the same way —
# this style is already used elsewhere in this package (plugins_page.go).
plant "BarePathHTTPError" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("/zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
}'
expect_fail "http.Error reached from a bare-path (no HTTP method) page route"
clear_fixture "BarePathHTTPError"

# A deliberate, explicitly allowlisted exception must be permitted.
plant "AllowlistedException" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError) // page-error:allow zz-guard-test fixture, not real
	})
}'
expect_pass "a line carrying an explicit page-error:allow comment"
clear_fixture "AllowlistedException"

# A POST-only / /api/-prefixed route is explicitly out of scope (an API
# endpoint, not a page an operator lands on) and must be left alone.
plant "APIRouteUntouched" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/zz-guard-test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
}'
expect_pass "http.Error in a POST /api/ handler (out of scope)"
clear_fixture "APIRouteUntouched"

# Baseline: the guard must still pass on the real, unmodified codebase —
# including the ~40 pre-existing (ut-docs#1458 scope) call sites, which
# carry page-error:allow annotations as of ut-docs#1455.
if ! bash "${GUARD}" >/tmp/guard_page_http_error_test_out.$$ 2>&1; then
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced)" >&2
  cat /tmp/guard_page_http_error_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard still passes on the clean codebase"
fi
rm -f /tmp/guard_page_http_error_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-page-http-error_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-page-http-error_test.sh: all cases passed"
exit 0
