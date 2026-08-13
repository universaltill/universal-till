#!/usr/bin/env bash
#
# Regression test for guard-docs-shots.sh's internal/pages/**.go surface
# filtering (ut-docs#620): a non-test .go file under internal/pages/ is
# excluded from the manual-screenshot surface ONLY when it registers at
# least one mux route and NONE of them is any help topic's routes[0] (the
# one URL docs-shots.spec.ts actually visits for that topic). Before this
# fix, EVERY non-test .go file in the package counted, which is what made a
# purely backend change (e.g. landing ut-docs#535 inside import_page.go's
# commit-loop logic, nowhere near its httpx.Render call) fail this guard for
# zero pixel reason — the exact case that got hand-patched around and
# prompted this ticket.
#
# A file with ZERO route registrations at all (a shared helper like
# init.go's baseMenu or themes.go's availableThemes) must stay INCLUDED —
# an earlier draft of this fix excluded those too and an independent review
# caught it as a real regression: such a file can still feed a screenshotted
# page's template data without registering a route itself.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-docs-shots.sh"
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
  local path="${FIXTURE_DIR}/zz_guard_docs_shots_test_${name}.go"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_docs_shots_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_docs_shots_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_docs_shots_test_out.$$
}

# Fails for the RIGHT reason, not just any reason — asserts the surface-
# staleness message specifically, so this doesn't false-pass on an
# unrelated guard failure (e.g. a missing manifest).
expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_docs_shots_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed (false negative)" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif grep -q "the app surface" /tmp/guard_docs_shots_test_out.$$; then
    echo "✓ guard correctly rejected ${label} (surface-staleness)"
  else
    echo "❌ FAIL: guard rejected ${label}, but not for surface staleness:" >&2
    cat /tmp/guard_docs_shots_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_docs_shots_test_out.$$
}

clear_fixture() {
  local name="$1"
  rm -f "${FIXTURE_DIR}/zz_guard_docs_shots_test_${name}.go"
  fixtures=()
}

# Baseline: the real, unmodified checkout must pass.
expect_pass "the real, unmodified checkout"

# A brand-new file registering a route NO help topic screenshots — the
# import_page.go class of case (routes exist, just none of them is any
# topic's routes[0]) — must NOT force a screenshot re-run.
plant "UnscreenshottedRoute" 'package pages

import "net/http"

func registerZZGuardTest(mux *http.ServeMux) {
	mux.HandleFunc("GET /zz-guard-test-not-screenshotted", func(w http.ResponseWriter, r *http.Request) {})
}'
expect_pass "a new file registering only a non-screenshotted route"
clear_fixture "UnscreenshottedRoute"

# A brand-new file with NO route registration at all (pure helper logic,
# e.g. init.go's baseMenu / themes.go's availableThemes shape) MUST still
# be treated as surface — it could be a shared helper feeding a
# screenshotted page's template data without registering any route of its
# own. This is the exact case an earlier draft of this fix got wrong.
plant "SharedHelperNoRoute" 'package pages

func zzGuardTestHelper(n int) int {
	return n * 2
}'
expect_fail "a new file with no mux route registration at all (shared-helper class)"
clear_fixture "SharedHelperNoRoute"

# A brand-new file registering a route that IS a screenshotted topic route
# (/reports, the "reports" topic) — this is a real surface change and MUST
# still be caught.
plant "ScreenshottedRoute" 'package pages

import "net/http"

func registerZZGuardTestReports(mux *http.ServeMux) {
	mux.HandleFunc("GET /reports", func(w http.ResponseWriter, r *http.Request) {})
}'
expect_fail "a new file registering a screenshotted route (/reports)"
clear_fixture "ScreenshottedRoute"

# Method-prefix-less registration style (this codebase uses both
# `mux.HandleFunc("/x", ...)` and `mux.HandleFunc("GET /x", ...)` — the
# route extraction must handle both, not just the GET-prefixed form).
plant "ScreenshottedRouteNoMethod" 'package pages

import "net/http"

func registerZZGuardTestBackoffice(mux *http.ServeMux) {
	mux.HandleFunc("/backoffice", func(w http.ResponseWriter, r *http.Request) {})
}'
expect_fail "a new file registering a screenshotted route without a method prefix (/backoffice)"
clear_fixture "ScreenshottedRouteNoMethod"

# Dotfiles must NOT be part of the surface (ut-docs#659). macOS drops a
# gitignored `.DS_Store` into any directory the Finder has browsed, so a
# developer's tree holds files CI's checkout never has. While those counted,
# `make docs-shots` on a Mac wrote a manifest whose surface_sha256 CI
# recomputed differently, and CI failed with "the app surface changed" against
# a diff in which nothing relevant had changed — no visible cause, and the
# obvious "just run make docs-shots again" does not fix it.
#
# Covers a dotfile in each surface root plus one inside a dot-DIRECTORY, since
# the fix has to prune both. Must stay in lockstep with
# e2e/tests-docs/lib.js's walkFiles(), which writes the manifest.
dotfile_fixtures=(
  "web/public/.ut-guard-fixture-dotfile"
  "web/ui/.ut-guard-fixture-dotfile"
  "${FIXTURE_DIR}/.ut-guard-fixture-dotfile.go"
  "web/public/.ut-guard-fixture-dir/planted.css"
)
mkdir -p "web/public/.ut-guard-fixture-dir"
for f in "${dotfile_fixtures[@]}"; do
  fixtures+=("${f}")
  printf 'planted by guard-docs-shots_test.sh\n' >"${f}"
done
expect_pass "gitignored dotfiles planted in the surface roots (.DS_Store class)"
for f in "${dotfile_fixtures[@]}"; do rm -f "${f}"; done
rmdir "web/public/.ut-guard-fixture-dir" 2>/dev/null || true
fixtures=()

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ all guard-docs-shots.sh regression cases passed"
