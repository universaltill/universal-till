#!/usr/bin/env bash
#
# Regression test for guard-kiosk-engine.sh (ut-docs#450): proves the guard
# actually flags a `d.Engine` reference planted inside a file that registers
# a /self-order or /api/self-order route, proves it does NOT false-positive
# on a comment merely mentioning "d.Engine" as prose (the ut-docs#449 style
# explanatory comment already in self_order_page.go), proves the explicit
# "kiosk-engine-guard:allow" escape hatch works, and proves the guard still
# passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-kiosk-engine.sh"
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
  if bash "${GUARD}" >/tmp/guard_kiosk_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_kiosk_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_kiosk_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_kiosk_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_kiosk_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_kiosk_test_out.$$
}

clear_fixture() {
  local name="$1"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}.go"
  fixtures=()
}

# A kiosk-route file that reaches d.Engine directly must be rejected — this
# is the exact bug class ut-docs#449 fixed and #450 exists to prevent a
# regression of.
plant "DirectEngineAccess" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/self-order/scan", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.Reset()
	})
}'
expect_fail "d.Engine reached from a /api/self-order handler"
clear_fixture "DirectEngineAccess"

# Same shape, but under a plain /self-order path (not /api/self-order) —
# the guard must cover both prefixes named in the card.
plant "DirectEngineAccessPlainPrefix" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /self-order", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.Reset()
	})
}'
expect_fail "d.Engine reached from a /self-order handler"
clear_fixture "DirectEngineAccessPlainPrefix"

# A comment that merely mentions "d.Engine" as prose (the real, existing
# style of explanatory comment in self_order_page.go) must NOT trip the
# guard — only actual code references count.
plant "CommentOnlyMention" 'package pages

import "net/http"

// KioskEngine, not Engine (ut-docs#449): this route must never touch
// d.Engine — only d.KioskEngine.
func registerZzGuardTest(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /self-order", func(w http.ResponseWriter, r *http.Request) {
		d.KioskEngine.Reset()
	})
}'
expect_pass "a comment that only mentions d.Engine as prose"
clear_fixture "CommentOnlyMention"

# A deliberate, explicitly allowlisted exception must be permitted.
plant "AllowlistedException" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /self-order/shop", func(w http.ResponseWriter, r *http.Request) {
		_ = d.Engine // kiosk-engine-guard:allow zz-guard-test fixture, not real cashier access
	})
}'
expect_pass "a line carrying an explicit kiosk-engine-guard:allow comment"
clear_fixture "AllowlistedException"

# A file that references d.Engine but registers no self-order route at all
# (the ordinary cashier-side pattern, e.g. pos_modifiers_api.go) must be
# left alone — this guard is scoped to kiosk routes only.
plant "NonKioskFileUntouched" 'package pages

import "net/http"

func registerZzGuardTest(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/pos/scan", func(w http.ResponseWriter, r *http.Request) {
		d.Engine.Reset()
	})
}'
expect_pass "d.Engine in a file that registers no /self-order route"
clear_fixture "NonKioskFileUntouched"

# Baseline: the guard must still pass on the real, unmodified codebase.
if ! bash "${GUARD}" >/tmp/guard_kiosk_test_out.$$ 2>&1; then
  echo "❌ FAIL: guard rejects the clean codebase (false positive introduced)" >&2
  cat /tmp/guard_kiosk_test_out.$$ >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard still passes on the clean codebase"
fi
rm -f /tmp/guard_kiosk_test_out.$$

if [[ "${FAIL_COUNT}" -gt 0 ]]; then
  echo "❌ guard-kiosk-engine_test.sh: ${FAIL_COUNT} case(s) failed" >&2
  exit 1
fi

echo "✓ guard-kiosk-engine_test.sh: all cases passed"
exit 0
