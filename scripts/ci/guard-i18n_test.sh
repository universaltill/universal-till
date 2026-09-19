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

# ut-docs#2423: a ternary whose branch is a raw literal was invisible to
# check 5 -- jsassign_re only matches a literal appearing directly as the
# RHS of the assignment, and a ternary's RHS starts with the condition
# (`ok`), not a quote.
plant "TernaryProse" '<script>
status.textContent = ok ? "Saved successfully" : "Save failed";
</script>'
expect_fail "hardcoded prose ternary branch"
clear_fixture "TernaryProse"

# Same ternary, marked i18n:ignore on the statement line -- the escape
# hatch must cover this shape identically.
plant "TernaryProseIgnored" '<script>
status.textContent = ok ? "Saved successfully" : "Save failed"; // i18n:ignore
</script>'
expect_pass "an i18n:ignore-marked ternary literal"
clear_fixture "TernaryProseIgnored"

# A ternary built entirely from translation lookups (the actual shape
# already shipped in base.html/settings.html/tax_codes.html) has no
# literal to flag at all -- must not false-positive on the identifiers.
plant "TernarySafe" '<script>
status.textContent = ok ? T.saved : T.failed;
</script>'
expect_pass "a ternary using translation variables (no literal)"
clear_fixture "TernarySafe"

# ut-docs#2423: a hardcoded prose literal returned from inside a .map()
# callback was invisible to check 5 -- the RHS right after
# `.innerHTML =` is `list`, not a quote, and the literal itself sits on a
# later line inside the callback body (the actual multi-line shape
# already shipped in promotions.html/settings.html/setup.html/tills.html
# and app.js, all safe today -- this proves a genuinely unsafe instance of
# the same shape would be caught).
plant "MapProse" '<script>
out.innerHTML = list.map(function (c) {
  return "<div>No results found</div>";
}).join("");
</script>'
expect_fail "hardcoded prose inside a .map() callback return"
clear_fixture "MapProse"

# Same .map() shape, but the callback only builds markup from escaped
# data -- the real pattern already shipped in this codebase -- must not
# false-positive.
plant "MapSafe" '<script>
out.innerHTML = list.map(function (c) {
  return "<button>" + esc(c.Name) + "</button>";
}).join(" ");
</script>'
expect_pass "a .map() callback building markup from escaped data only"
clear_fixture "MapSafe"

# Same unsafe .map() shape, marked i18n:ignore on the return line itself
# -- the escape hatch must reach inside the callback body, not just the
# assignment line.
plant "MapProseIgnored" '<script>
out.innerHTML = list.map(function (c) {
  return "<div>No results found</div>"; // i18n:ignore
}).join("");
</script>'
expect_pass "an i18n:ignore-marked map() literal"
clear_fixture "MapProseIgnored"

# A single-line arrow .map() returning a literal directly (no braced
# body) must be caught too -- the arrow-shorthand variant of the same gap.
plant "MapArrowProse" '<script>
out.innerHTML = list.map(c => "No results found").join("");
</script>'
expect_fail "hardcoded prose in a single-line arrow .map() literal"
clear_fixture "MapArrowProse"

# ut-docs#2423 independent review (N1): strip_markup's unmatched-"<"
# heuristic must not swallow a GENUINE "<" in real prose -- only a
# trailing "<" that is actually tag-shaped (immediately followed by a
# letter/slash/bang) may be treated as a truncated open tag. Before this
# fix, "Discount < 5 percent is not allowed" silently stopped flagging,
# because appending a synthetic ">" closed the whole rest of the
# sentence as if it were one giant tag.
plant "LessThanProse" '<script>
msg.textContent = "Discount < 5 percent is not allowed";
</script>'
expect_fail "a genuine \"<\" inside real prose (not a truncated tag)"
clear_fixture "LessThanProse"

# ut-docs#2423 independent review (N3): strip_markup's PARTIAL-tag-concat
# branch (the actual reason it exists) needs its own fixture -- the
# original InnerHTMLMarkupOnly fixture above only exercises a COMPLETE
# tag, which never enters that branch at all. Mirrors the real shape
# already shipped in promotions.html/settings.html/setup.html/tills.html:
# a .map() callback's first literal segment is a truncated open tag with
# no closing ">", built up via concatenation with escaped data.
plant "MapPartialTagConcat" '<script>
out.innerHTML = list.map(function (c) {
  return "<button type=\"button\" data-cust-pick=\"" + esc(c.ID) + "\">" + esc(c.Name) + "</button>";
}).join(" ");
</script>'
expect_pass "a .map() callback building a tag via concatenation (no real prose)"
clear_fixture "MapPartialTagConcat"

# ut-docs#2423 independent review (N2): map_close_re must recognise a
# close style other than `}).join(...)` on the exact same line --
# `}, this).join(...)`, `}.bind(this))...`, or the closing `)` on its own
# following line are all real JS. Before this fix, none of those matched
# `^\s*\}\)`, so the bounded lookahead ran straight past the callback and
# flagged an unrelated function's own return -- a confusing false
# positive pointing at code with no .innerHTML/.textContent anywhere near
# it.
plant "MapCloseThisArg" '<script>
out.innerHTML = rows.map(function (r) {
  return render(r);
}, this).join("");
function helper() {
  return "Something went wrong";
}
</script>'
expect_pass "a .map() whose close style the bounded lookahead must not spill past"
clear_fixture "MapCloseThisArg"

# Sanity: the guard must still pass clean on the real, unmodified tree
# (proves this test file itself, and the fixtures' cleanup, leave no
# residue behind).
expect_pass "the real, unmodified repository tree"

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ ${FAIL_COUNT} guard-i18n_test.sh case(s) failed" >&2
  exit 1
fi
echo "✓ guard-i18n_test.sh: all cases passed"
