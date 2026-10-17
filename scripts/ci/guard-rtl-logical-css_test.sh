#!/usr/bin/env bash
#
# Regression test for guard-rtl-logical-css.sh (ut-docs#4375, ADR-0160 §8):
# runs the guard against a fixture web/ tree in a fresh mktemp dir and proves
# it rejects physical left/right CSS in a .css file, a template <style> block
# and a style="" attribute (one-line and multi-line), ignores JS in a
# template, comments, logical properties and look-alike names, honours
# `rtl:allow` and the baseline, fails on a stale baseline entry, skips
# web/public/vendor, and passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-rtl-logical-css.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

FAKE="${WORK}/repo"
UI="${FAKE}/web/ui"
PUB="${FAKE}/web/public"
BASELINE="${WORK}/baseline.txt"

reset_tree() {
  rm -rf -- "${FAKE:?}"
  mkdir -p "${UI}/partials" "${UI}/layouts" "${PUB}/vendor" "${PUB}/themes"
  printf '%s\n' '<style>.a { margin-inline-start: 1rem; }</style>' '<div style="padding-inline-end: 2px"></div>' >"${UI}/layouts/base.html"
  printf '%s\n' '.b { inset-inline-start: 0; text-align: start; }' >"${PUB}/app.css"
  : >"${BASELINE}"
}

run_guard() { bash "${GUARD}" "${FAKE}" "${BASELINE}" >"${WORK}/out" 2>&1; }

expect_fail() {
  if run_guard; then
    echo "❌ FAIL: expected the guard to reject $1, but it passed" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard rejected $1"
  fi
}

expect_pass() {
  if run_guard; then
    echo "✓ guard ignored $1"
  else
    echo "❌ FAIL: expected the guard to ignore $1 (false positive), but it rejected it" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

reset_tree
expect_pass "a clean fixture tree"

# --- .css files -------------------------------------------------------------
for decl in 'margin-left: 4px' 'padding-right:0' 'border-left: 1px solid' \
  'border-right-color: red' 'border-top-left-radius: 4px' 'left: 0' \
  'right:12px' 'text-align: left' 'float: right' 'clear: left' \
  'scroll-margin-left: 2px' 'MARGIN-LEFT: 1px'; do
  reset_tree
  printf '.x { %s; }\n' "${decl}" >>"${PUB}/app.css"
  expect_fail "'${decl}' in a .css file"
done

reset_tree
printf '%s\n' '.x {' '  color: red;' '  margin-right: 1rem;' '}' >"${PUB}/themes/dark.css"
expect_fail "a physical property on its own line in a theme .css"

reset_tree
printf '%s\n' '.x{left:0}' >>"${PUB}/app.css"
expect_fail "a minified '{left:0}' declaration"

# --- templates --------------------------------------------------------------
reset_tree
printf '%s\n' '<style>' '  .x { padding-left: 0; }' '</style>' >"${UI}/partials/p.html"
expect_fail "'padding-left' in a template <style> block"

reset_tree
printf '%s\n' '<div style="margin-left: 4px">x</div>' >"${UI}/partials/p.html"
expect_fail "'margin-left' in a one-line style attribute"

reset_tree
printf '%s\n' "<div style='right: 0'>x</div>" >"${UI}/partials/p.html"
expect_fail "'right' in a single-quoted style attribute"

reset_tree
printf '%s\n' '<div id="r" style="' '  position:absolute;' '  left:0;' '"></div>' >"${UI}/partials/p.html"
expect_fail "'left' in a multi-line style attribute"

reset_tree
printf '%s\n' '<style>.y { color: red; }' '.x { float: left; }</style>' >"${UI}/partials/p.html"
expect_fail "a second line of a <style> block opened mid-line"

reset_tree
printf '%s\n' '<!doctype html><html><body>' '<div style="margin-inline-start:1px">x</div>' >"${PUB}/page.html"
printf '%s\n' '<div style="padding-left:1px">x</div>' >>"${PUB}/page.html"
expect_fail "a style attribute in a web/public .html page"

reset_tree
printf '%s\n' '<input type="file" accept="image/*">' '<div style="margin-left:1px">x</div>' >"${UI}/partials/p.html"
expect_fail "a style attribute after accept=\"image/*\" (glob is not a comment)"

reset_tree
printf '%s\n' '<div style="color:red;{{ if .E }}margin-left:4px{{ end }}">x</div>' >"${UI}/partials/p.html"
expect_fail "a declaration right after a Go template action"

reset_tree
printf '%s\n' '<div style="{{ if .E }}border-left:2px solid red{{ end }}">x</div>' >"${UI}/partials/p.html"
expect_fail "a declaration opening inside a Go template action"

reset_tree
printf '%s\n' '<div style="margin-left:1px">x</div> <!-- rtl:allow fixture -->' >"${UI}/partials/p.html"
expect_pass "rtl:allow in a template"

reset_tree
printf '%s\n' '<STYLE media="print">' '.x { LEFT: 0 }' '</STYLE>' >"${UI}/partials/p.html"
expect_fail "an uppercase <STYLE media=> block"

# --- false positives --------------------------------------------------------
reset_tree
printf '%s\n' '<script>' '  var box = { left: r.left, right: r.right };' '  el.style.left = x + "px";' '</script>' >"${UI}/partials/p.html"
expect_pass "JS object keys and el.style.left in a template <script>"

reset_tree
printf '%s\n' '<p>Swipe left: to go back</p>' '<button data-side="left">x</button>' >"${UI}/partials/p.html"
expect_pass "plain text and attributes outside any CSS context"

reset_tree
printf '%s\n' '<script>el.style = "left: " + x; </script>' '<div :style="{ left: x }"></div>' >"${UI}/partials/p.html"
expect_pass "el.style= and :style= bindings (JS, not a style attribute)"

reset_tree
printf '%s\n' '/* margin-left: 0 was replaced by margin-inline-start */' '.x {' '  /* left: 0 */ color: red;' '}' >>"${PUB}/app.css"
expect_pass "physical properties inside CSS comments"

reset_tree
printf '%s\n' '/* a block comment' '   left: 0;' '   still the comment */' '.x { color: red; }' >>"${PUB}/app.css"
expect_pass "a physical property inside a multi-line CSS comment"

reset_tree
printf '%s\n' '<!-- <div style="left:0"> -->' '{{/* style="margin-left:1px" */}}' >"${UI}/partials/p.html"
expect_pass "HTML and Go-template comments"

reset_tree
printf '%s\n' '.x { margin-inline-start: 0; padding-inline-end: 0; inset-inline: 0; border-inline-start: 1px solid; text-align: end; float: inline-start; }' >>"${PUB}/app.css"
expect_pass "logical properties"

reset_tree
printf '%s\n' '.left:hover { color: red; }' '.x[data-side="left"] { color: red; }' '.x { transform-origin: left; background-position: right; }' >>"${PUB}/app.css"
expect_pass "selectors and out-of-scope properties naming left/right"

reset_tree
printf '%s\n' '.x { margin-left: 0; }' >"${PUB}/vendor/lib.css"
expect_pass "web/public/vendor"

# --- exceptions ---------------------------------------------------------------
reset_tree
printf '%s\n' '.t { left: 50%; transform: translateX(-50%); } /* rtl:allow centring, symmetric */' >>"${PUB}/app.css"
expect_pass "a same-line rtl:allow marker"

reset_tree
printf '%s\n' '.t { left: 50%; transform: translateX(-50%); }' >>"${PUB}/app.css"
printf 'web/public/app.css\t%s\n' '.t { left: 50%; transform: translateX(-50%); }' >"${BASELINE}"
expect_pass "a baselined line"

reset_tree
printf '%s\n' '  .t { left: 50%; }' >>"${PUB}/app.css"
printf 'web/public/app.css\t%s\n' '.t { left: 50%; }' >"${BASELINE}"
expect_pass "a baselined line matched with its indentation trimmed"

reset_tree
printf '%s\n' '.t { left: 50%; }' >"${PUB}/themes/dark.css"
printf 'web/public/app.css\t%s\n' '.t { left: 50%; }' >"${BASELINE}"
expect_fail "a baseline entry for another file (keyed on path)"

reset_tree
printf 'web/public/app.css\t%s\n' '.gone { left: 0; }' >"${BASELINE}"
expect_fail "a stale baseline entry (shrink-only: the fixed line must leave the baseline)"

reset_tree
printf '%s\n' '.t { left: 50%; }' '.t { left: 50%; }' >>"${PUB}/app.css"
printf 'web/public/app.css\t%s\n' '.t { left: 50%; }' >"${BASELINE}"
expect_fail "a second copy of a line baselined once"

reset_tree
printf '%s\n' '.t { left: 50%; }' '.t { left: 50%; }' >>"${PUB}/app.css"
printf 'web/public/app.css\t%s\n' '.t { left: 50%; }' '.t { left: 50%; }' >"${BASELINE}"
expect_pass "two copies baselined twice"

reset_tree
printf '%s\n' '.t { left: 50%; }' >>"${PUB}/app.css"
printf 'web/public/app.css\t%s\n' '.t { left: 50%; }' '.t { left: 50%; }' >"${BASELINE}"
expect_fail "a baseline listing more copies than remain (stale)"

reset_tree
printf '%s\n' 'no tab here' >"${BASELINE}"
expect_fail "a malformed baseline line"

reset_tree
printf '%s\n' '# comment line' '' >"${BASELINE}"
expect_pass "baseline comments and blank lines"

# --- the real tree --------------------------------------------------------------
if bash "${GUARD}" >"${WORK}/out" 2>&1; then
  echo "✓ guard passes on the real tree"
else
  echo "❌ FAIL: guard rejects the real tree" >&2
  cat "${WORK}/out" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "❌ ${FAIL_COUNT} guard-rtl-logical-css regression case(s) failed" >&2
  exit 1
fi
echo "✓ guard-rtl-logical-css regression test passed"
