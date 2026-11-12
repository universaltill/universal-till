#!/usr/bin/env bash
#
# Regression test for guard-no-native-confirm.sh (ut-docs#6116): runs the
# guard against fixture ui/public trees in a fresh mktemp dir and proves it
# rejects every spelling of a native confirm call, ignores UT.confirm,
# hx-confirm, data-confirm*, object keys and every comment form, honours
# `native-confirm:allow <reason>` (and rejects it without a reason), skips
# web/public/vendor, and passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-no-native-confirm.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

UI="${WORK}/ui"
PUB="${WORK}/public"

reset_tree() {
  rm -rf -- "${UI:?}" "${PUB:?}"
  mkdir -p "${UI}/partials" "${UI}/pages" "${PUB}/vendor"
  printf '%s\n' '<dialog id="x"></dialog>' >"${UI}/pages/base.html"
  printf '%s\n' 'document.getElementById("x").show();' >"${PUB}/app.js"
}

run_guard() { bash "${GUARD}" "${UI}" "${PUB}" >"${WORK}/out" 2>&1; }

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

reset_tree
printf '%s\n' "if (window.confirm('x')) go();" >"${PUB}/app.js"
expect_fail "a window.confirm() call in web/public"

reset_tree
printf '%s\n' "<button @click=\"if (confirm('x')) go()\">x</button>" >"${UI}/pages/p.html"
expect_fail "a bare confirm() inside an Alpine @click"

reset_tree
printf '%s\n' "if (confirm ('x')) go();" >"${PUB}/app.js"
expect_fail "a confirm () call with a space"

reset_tree
printf '%s\n' "window['confirm']('x');" >"${PUB}/app.js"
expect_fail "a bracket-access confirm call"

reset_tree
printf '%s\n' "confirm.call(window, 'x');" >"${PUB}/app.js"
expect_fail "a confirm.call() call"

reset_tree
printf '%s\n' "self.confirm('x');" "globalThis.confirm('y');" >"${PUB}/app.js"
expect_fail "self.confirm and globalThis.confirm calls"

reset_tree
printf '%s\n' 'var c = window.confirm;' >"${PUB}/app.js"
expect_fail "a window.confirm alias"

reset_tree
printf '%s\n' "x(); confirm('y'); // ok" >"${PUB}/app.js"
expect_fail "a call followed by a trailing comment"

reset_tree
printf '%s\n' "window.confirm('x'); // native-confirm:allow" >"${PUB}/app.js"
expect_fail "a native-confirm:allow marker with no reason"

reset_tree
printf '%s\n' "window.confirm('x'); // native-confirm:allow test fallback" >"${PUB}/app.js"
expect_pass "a native-confirm:allow marker with a reason"

reset_tree
printf '%s\n' "UT.confirm('x');" "window.UT.confirm('y', { opener: b });" >"${PUB}/app.js"
expect_pass "UT.confirm and window.UT.confirm"

reset_tree
printf '%s\n' '<button hx-confirm="x" data-confirm-text="y">x</button>' >"${UI}/pages/p.html"
expect_pass "hx-confirm and data-confirm-text attributes"

reset_tree
printf '%s\n' "var a = { 'confirm': function (ctx) {} };" >"${PUB}/app.js"
expect_pass "an object key named confirm"

reset_tree
cat >"${PUB}/app.js" <<'EOS'
// window.confirm() is not used here
x(); // confirm() would be wrong
/* never confirm()
   across confirm() lines */
d.show();
EOS
expect_pass "JS line, trailing and block comments"

reset_tree
cat >"${UI}/partials/comments.html" <<'EOS'
<!-- never confirm() -->
<!-- spans
     confirm()
     lines -->
{{/* template
     confirm()
     comment */}}
EOS
expect_pass "HTML and Go-template comments across lines"

reset_tree
printf '%s\n' "window.confirm('x');" >"${PUB}/vendor/lib.js"
expect_pass "web/public/vendor"

reset_tree
printf '%s\n' "confirm('x')" >"${PUB}/app.css"
expect_pass "a non-.html/.js file"

if bash "${GUARD}" "${WORK}/missing" "${PUB}" >"${WORK}/out" 2>&1; then
  echo "❌ FAIL: expected the guard to reject a missing directory" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ guard rejected a missing directory"
fi

if bash "${GUARD}" >"${WORK}/out" 2>&1; then
  echo "✓ guard passes on the real tree"
else
  echo "❌ FAIL: guard rejects the real tree" >&2
  cat "${WORK}/out" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if [ "${FAIL_COUNT}" -gt 0 ]; then
  echo "❌ ${FAIL_COUNT} guard-no-native-confirm test(s) failed" >&2
  exit 1
fi
echo "✓ guard-no-native-confirm: all tests passed"
