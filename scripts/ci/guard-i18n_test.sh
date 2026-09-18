#!/usr/bin/env bash
#
# Regression test for guard-i18n.sh's check 5 (ut-docs#205): proves the
# guard actually flags a hardcoded prose string assigned to .textContent/
# .innerHTML inside a <script> block, proves the i18n:ignore escape hatch
# works, proves a markup-only literal built up in JS (no real prose, just
# tag/attribute skeleton) does NOT false-positive after tag-stripping, and
# proves a real prose literal wrapped in a tag still correctly flags. Also
# proves the guard still passes on the real, unmodified codebase.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-i18n.sh"
FIXTURE_DIR="web/ui/pages"
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
  local path="${FIXTURE_DIR}/zz_guard_test_${name}.html"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_i18n_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_i18n_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_i18n_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_i18n_test_out.$$
}

clear_fixture() {
  local name="$1"
  rm -f "${FIXTURE_DIR}/zz_guard_test_${name}.html"
  fixtures=()
}

JS_FIXTURE_DIR="web/public"
VENDOR_FIXTURE_DIR="web/public/vendor"

plant_js() {
  local name="$1" content="$2"
  local path="${JS_FIXTURE_DIR}/zz_guard_test_${name}.js"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

plant_vendor_js() {
  local name="$1" content="$2"
  local path="${VENDOR_FIXTURE_DIR}/zz_guard_test_${name}.js"
  fixtures+=("${path}")
  printf '%s\n' "${content}" >"${path}"
}

clear_js_fixture() {
  local name="$1"
  rm -f "${JS_FIXTURE_DIR}/zz_guard_test_${name}.js"
  fixtures=()
}

clear_vendor_fixture() {
  local name="$1"
  rm -f "${VENDOR_FIXTURE_DIR}/zz_guard_test_${name}.js"
  fixtures=()
}

# A hardcoded prose string assigned to .textContent inside a <script> block
# must be rejected — the exact bug class ut-docs#205 found (settings.html's
# pre-existing "pick a from/to date").
plant "TextContentProse" '<script>
msg.textContent = "pick a from/to date";
</script>'
expect_fail "hardcoded prose .textContent literal"
clear_fixture "TextContentProse"

# Same literal, but marked i18n:ignore on the same line — the established
# escape hatch (already used by check 3) must exempt it.
plant "TextContentProseIgnored" '<script>
msg.textContent = "pick a from/to date"; // i18n:ignore
</script>'
expect_pass "an i18n:ignore-marked hardcoded literal"
clear_fixture "TextContentProseIgnored"

# A markup skeleton being assembled via innerHTML, no real prose in it (the
# exact false-positive the tag-stripping step in check 5 exists to avoid --
# independent review would otherwise flag "ul style" out of the attribute
# name, same shape as the real setup.html/tills.html code).
plant "InnerHTMLMarkupOnly" '<script>
out.innerHTML = "<ul style=\"list-style:none; padding-inline-start:0\">";
</script>'
expect_pass "a markup-only innerHTML literal with no real prose"
clear_fixture "InnerHTMLMarkupOnly"

# A real prose literal wrapped in a tag must still be caught after
# stripping the tag around it (the exact shape of settings.html's own
# pre-existing "<p class=\"muted\">No matches.</p>").
plant "InnerHTMLTaggedProse" '<script>
out.innerHTML = "<p class=\"muted\">No matches found here.</p>";
</script>'
expect_fail "a real prose literal wrapped in a tag"
clear_fixture "InnerHTMLTaggedProse"

# Single-word / symbol-only status text (no whitespace-separated word pair)
# is a known, accepted heuristic gap -- must NOT flag, same recall/false-
# positive tradeoff the Go-side check 3 already documents.
plant "SingleWordStatus" '<script>
msg.textContent = "saving…";
</script>'
expect_pass "a single-word status literal (accepted heuristic gap)"
clear_fixture "SingleWordStatus"

# Check 5's glob now also covers shipped JS under web/public/ (ut-docs#453
# follow-up to #205 — the original card's own header comment flagged this
# as a known gap: real prose in web/public/app.js was invisible to check 5
# because it only ever globbed web/ui/**/*.html). A hardcoded prose literal
# assigned to .textContent/.innerHTML in a plain .js file under web/public/
# must now be caught the same way it would be inside a <script> block.
plant_js "TextContentProse" 'msg.textContent = "pick a from/to date";'
expect_fail "hardcoded prose .textContent literal in web/public/*.js"
clear_js_fixture "TextContentProse"

# Same web/public/*.js literal, marked i18n:ignore -- the escape hatch must
# work identically outside web/ui/ too.
plant_js "TextContentProseIgnored" 'msg.textContent = "pick a from/to date"; // i18n:ignore'
expect_pass "an i18n:ignore-marked literal in web/public/*.js"
clear_js_fixture "TextContentProseIgnored"

# web/public/vendor/ (third-party, unmodified libraries) must stay excluded
# -- the same reason internal/**/*.go checks never scan a vendor directory.
plant_vendor_js "TextContentProse" 'msg.textContent = "pick a from/to date";'
expect_pass "a prose literal inside web/public/vendor/ (excluded)"
clear_vendor_fixture "TextContentProse"

# Sanity: the guard must still pass clean on the real, unmodified tree
# (proves this test file itself, and the fixtures' cleanup, leave no
# residue behind).
expect_pass "the real, unmodified repository tree"

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ ${FAIL_COUNT} guard-i18n_test.sh case(s) failed" >&2
  exit 1
fi
echo "✓ guard-i18n_test.sh: all cases passed"
