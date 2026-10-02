#!/usr/bin/env bash
#
# Regression test for guard-no-inline-handlers.sh (ut-docs#3325, CSP slice
# 2): proves the guard rejects every inline-handler shape it exists to keep
# out of web/ui (an onclick=, one with whitespace before its `=`, an
# hx-on::after-request=, a legacy hx-on="htmx:..." attribute, an upper-case
# ONCLICK=, one spelled inside a comment), honours an exact file:line
# allowlist entry and nothing looser, rejects any data-on* attribute name
# (html/template JS-escapes those -- the tester-pass regression),
# rejects a standalone document that never loads inline-actions.js, rejects
# inline-actions.js with any of its delegated listeners removed (in real
# code, not just in a comment), and passes both a minimal good fixture set
# and the real, unmodified repo tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-no-inline-handlers.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT
OUT="${TMPDIR}/guard_out.txt"

GOOD_JS="${TMPDIR}/inline-actions.js"
cat >"${GOOD_JS}" <<'EOF'
(function () {
  document.addEventListener('click', function () {}, true);
  document.addEventListener('error', function () {}, true);
  document.addEventListener('htmx:afterRequest', function () {}, true);
  document.addEventListener('htmx:beforeRequest', function () {}, true);
})();
EOF

EMPTY_ALLOW="${TMPDIR}/allow_empty.txt"
printf '# no entries\n' >"${EMPTY_ALLOW}"

# A fresh fixture tree: one standalone document that loads
# inline-actions.js, plus a converted partial.
fresh_ui_dir() {
  local dir="${TMPDIR}/ui_$1"
  mkdir -p "${dir}/layouts" "${dir}/partials"
  cat >"${dir}/layouts/base.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/inline-actions.js?v=1"></script></head><body></body></html>
EOF
  cat >"${dir}/partials/ok.html" <<'EOF'
<button type="button" data-action="close:x-modal">Close</button>
<form hx-post="/x" data-after-request="ok close:x-modal"></form>
<img src="/a.png" alt="" data-fallback="hide">
EOF
  printf '%s' "${dir}"
}

expect_pass() {
  local label="$1"; shift
  if bash "${GUARD}" "$@" >"${OUT}" 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

expect_fail() {
  local label="$1"; shift
  if bash "${GUARD}" "$@" >"${OUT}" 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat "${OUT}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
}

ui_ok="$(fresh_ui_dir ok)"
expect_pass "a converted fixture tree" "${ui_ok}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_onclick="$(fresh_ui_dir onclick)"
echo '<button onclick="document.getElementById(&#39;m&#39;).close()">x</button>' >>"${ui_onclick}/partials/ok.html"
expect_fail "an onclick= attribute" "${ui_onclick}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_onerror="$(fresh_ui_dir onerror)"
echo '<img src="/a.png" alt="" onerror="this.style.visibility=&#39;hidden&#39;">' >>"${ui_onerror}/partials/ok.html"
expect_fail "an onerror= attribute" "${ui_onerror}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_upper="$(fresh_ui_dir upper)"
echo '<button ONCLICK="go()">x</button>' >>"${ui_upper}/partials/ok.html"
expect_fail "an upper-case ONCLICK= attribute (HTML attributes are case-insensitive)" "${ui_upper}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# HTML allows whitespace around an attribute's `=`; `onclick ="..."` is a
# live handler the browser runs exactly like `onclick="..."`, so a pattern
# that only matched the name glued to its `=` was a bypass (ut-docs#3325
# review).
ui_space_eq="$(fresh_ui_dir spaceeq)"
echo '<button onclick ="go()">x</button>' >>"${ui_space_eq}/partials/ok.html"
expect_fail "an onclick= attribute with a space before its =" "${ui_space_eq}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_tab_eq="$(fresh_ui_dir tabeq)"
printf '<img src="/a.png" alt="" onerror\t="go()">\n' >>"${ui_tab_eq}/partials/ok.html"
expect_fail "an onerror= attribute with a tab before its =" "${ui_tab_eq}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_hxon="$(fresh_ui_dir hxon)"
echo '<form hx-post="/x" hx-on::after-request="this.reset()"></form>' >>"${ui_hxon}/partials/ok.html"
expect_fail "an hx-on::after-request= attribute" "${ui_hxon}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_hxon_legacy="$(fresh_ui_dir hxonlegacy)"
echo '<button hx-on="htmx:afterRequest: go()">x</button>' >>"${ui_hxon_legacy}/partials/ok.html"
expect_fail "a legacy hx-on=\"htmx:...\" attribute" "${ui_hxon_legacy}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# The guard is deliberately a raw grep: a handler spelled out in a comment
# (or in a <script> string literal that builds markup) still fails, so a
# lexical comment-stripper can never be fooled into hiding a real one.
ui_comment="$(fresh_ui_dir comment)"
echo '<!-- the old hx-on::after-request closed it -->' >>"${ui_comment}/partials/ok.html"
expect_fail "an hx-on mention inside a comment" "${ui_comment}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_jsstr="$(fresh_ui_dir jsstr)"
cat >>"${ui_jsstr}/partials/ok.html" <<'EOF'
<script>var html = '<button ' + 'onclick="x()">' + '</button>';</script>
EOF
expect_fail "an onclick= built inside a <script> string" "${ui_jsstr}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# Allowlist: the exact file:line passes; the same file on another line,
# or a bare file name, does not.
allow_exact="${TMPDIR}/allow_exact.txt"
printf '# reviewed: fixture\nweb/ui/partials/ok.html:4\n' >"${allow_exact}"
expect_pass "an allowlisted exact file:line" "${ui_onclick}" "${GOOD_JS}" "${allow_exact}"

allow_wrong_line="${TMPDIR}/allow_wrong_line.txt"
printf 'web/ui/partials/ok.html:3\n' >"${allow_wrong_line}"
expect_fail "an allowlist entry for a different line of the same file" "${ui_onclick}" "${GOOD_JS}" "${allow_wrong_line}"

allow_file_only="${TMPDIR}/allow_file_only.txt"
printf 'web/ui/partials/ok.html\n' >"${allow_file_only}"
expect_fail "an allowlist entry naming only the file" "${ui_onclick}" "${GOOD_JS}" "${allow_file_only}"

# A standalone document that never loads inline-actions.js: every data-*
# hook in it would be dead.
ui_noload="$(fresh_ui_dir noload)"
cat >"${ui_noload}/layouts/setup.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/vendor/htmx.min.js"></script></head><body></body></html>
EOF
expect_fail "a standalone document that never loads inline-actions.js" "${ui_noload}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_noload_comment="$(fresh_ui_dir noloadcomment)"
cat >"${ui_noload_comment}/layouts/setup.html" <<'EOF'
<!DOCTYPE html>
<html><head><!-- <script defer src="/public/inline-actions.js"></script> --></head><body></body></html>
EOF
expect_fail "a standalone document with inline-actions.js commented out" "${ui_noload_comment}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# A data-on* attribute name: html/template would JS-escape any {{ }} inside
# it (the ut-docs#3325 tester-pass regression), whatever it is used for.
ui_data_on="$(fresh_ui_dir dataon)"
echo '<form hx-post="/x" data-on-after-request="fail unhide:pin-error-1"></form>' >>"${ui_data_on}/partials/ok.html"
expect_fail "a data-on-after-request= attribute (html/template JS-escapes data-on*)" "${ui_data_on}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_data_only="$(fresh_ui_dir dataonly)"
echo '<div data-only="x"></div>' >>"${ui_data_only}/partials/ok.html"
expect_fail "a data-only= attribute (strips to \"only\", same \"on\" prefix)" "${ui_data_only}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_data_action="$(fresh_ui_dir dataaction)"
echo '<button data-action="close:x" data-confirm-text="y" data-done-text="z">x</button>' >>"${ui_data_action}/partials/ok.html"
expect_pass "data-action / data-*-text attributes (no on prefix after data-)" "${ui_data_action}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# inline-actions.js with each delegated listener removed in turn.
for ev in click error htmx:afterRequest htmx:beforeRequest; do
  js="${TMPDIR}/missing_${ev//:/_}.js"
  grep -vF "'${ev}'" "${GOOD_JS}" >"${js}"
  expect_fail "inline-actions.js without its '${ev}' listener" "${ui_ok}" "${js}" "${EMPTY_ALLOW}"
done

commented_js="${TMPDIR}/commented.js"
cat >"${commented_js}" <<'EOF'
// document.addEventListener('click', f, true);
// document.addEventListener('error', f, true);
/* document.addEventListener('htmx:afterRequest', f, true);
   document.addEventListener('htmx:beforeRequest', f, true); */
(function () {})();
EOF
expect_fail "inline-actions.js whose listeners only appear inside comments" "${ui_ok}" "${commented_js}" "${EMPTY_ALLOW}"

expect_fail "a nonexistent UI directory" "${TMPDIR}/does-not-exist" "${GOOD_JS}" "${EMPTY_ALLOW}"
expect_fail "a nonexistent inline-actions.js" "${ui_ok}" "${TMPDIR}/does-not-exist.js" "${EMPTY_ALLOW}"
expect_fail "a nonexistent allowlist" "${ui_ok}" "${GOOD_JS}" "${TMPDIR}/does-not-exist.txt"

# The real, unmodified tree must pass.
expect_pass "the real repo tree"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-no-inline-handlers_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-no-inline-handlers_test.sh: all assertions passed"
