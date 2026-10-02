#!/usr/bin/env bash
#
# Regression test for guard-no-inline-handlers.sh (ut-docs#3325): proves the
# guard rejects an inline on*= handler (also at line start, after a Go
# action or a closing quote, and upper-case) and an hx-on attribute, leaves
# near-misses alone (`var online =`, hx-on in prose, data-on-*), ignores
# mentions inside HTML / Go-template / line JS comments (without shifting
# line numbers), honours exact file:line allowlist entries only (a missing
# allowlist exempts nothing), rejects an inline-actions.js whose delegation
# listeners are gone (or only mentioned in comments), and fails closed on
# missing inputs.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-no-inline-handlers.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT
OUT="${TMPDIR}/out.txt"

GOOD_JS="${TMPDIR}/inline-actions.js"
cat >"${GOOD_JS}" <<'EOF'
(function () {
  document.addEventListener('click', function () {});
  document.addEventListener('error', function () {}, true);
  document.body.addEventListener('htmx:afterRequest', function () {});
  document.body.addEventListener('htmx:beforeRequest', function () {});
})();
EOF
EMPTY_ALLOW="${TMPDIR}/allow_empty.txt"
: >"${EMPTY_ALLOW}"

fresh_ui_dir() {
  local dir="${TMPDIR}/ui_$1"
  mkdir -p "${dir}/partials"
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

# Clean markup: data-* attributes only, plus comments that merely MENTION
# the forbidden attributes (HTML, Go-template and line-JS comments).
ui_ok="$(fresh_ui_dir ok)"
cat >"${ui_ok}/partials/clean.html" <<'EOF'
<!-- the old hx-on::after-request and onclick="x()" lived here -->
{{/* used to be onclick="close()" —
     and hx-on::before-request too */}}
<button type="button" data-action="close-dialog" data-target="#m">x</button>
<img src="a.png" alt="" data-fallback="hide">
<script>
  // hx-on::after-request reload pattern, described in a comment only
  var x = 1;
  /* the tile's own
     hx-on::response-error/send-error, in a block comment */
</script>
EOF
expect_pass "clean markup whose only mentions are in comments" "${ui_ok}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_onclick="$(fresh_ui_dir onclick)"
cat >"${ui_onclick}/partials/a.html" <<'EOF'
<button type="button" onclick="document.getElementById('m').close()">x</button>
EOF
expect_fail "an inline onclick= handler" "${ui_onclick}" "${GOOD_JS}" "${EMPTY_ALLOW}"
if grep -qF 'partials/a.html:1' "${OUT}"; then
  echo "✓ guard names the violating file:line"
else
  echo "❌ FAIL: guard output did not name partials/a.html:1" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

ui_onerror="$(fresh_ui_dir onerror)"
printf '<img src="a.png" alt=""\n     onerror="this.style.visibility=%shidden%s">\n' "'" "'" >"${ui_onerror}/partials/img.html"
expect_fail "a multi-line tag with an inline onerror= handler" "${ui_onerror}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# Attribute shapes that have no whitespace in front of the name, or an
# upper-case name — all valid HTML that CSP still blocks.
ui_col0="$(fresh_ui_dir col0)"
printf '<button\nonclick="x()">x</button>\n' >"${ui_col0}/partials/c.html"
expect_fail "an on*= attribute at the start of a line" "${ui_col0}" "${GOOD_JS}" "${EMPTY_ALLOW}"
ui_action="$(fresh_ui_dir action)"
cat >"${ui_action}/partials/t.html" <<'EOF'
<a href="/items"{{ if .InShell }}onclick="this.closest('dialog').close()"{{ end }}>x</a>
EOF
expect_fail "an on*= attribute directly after a Go template action" "${ui_action}" "${GOOD_JS}" "${EMPTY_ALLOW}"
ui_quote="$(fresh_ui_dir quote)"
cat >"${ui_quote}/partials/q.html" <<'EOF'
<button class="btn"onclick="go()">x</button>
EOF
expect_fail "an on*= attribute directly after a quoted value" "${ui_quote}" "${GOOD_JS}" "${EMPTY_ALLOW}"
ui_upper="$(fresh_ui_dir upper)"
cat >"${ui_upper}/partials/u.html" <<'EOF'
<button ONCLICK="go()">x</button>
EOF
expect_fail "an upper-case ONCLICK= attribute" "${ui_upper}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# Not handlers: a script variable whose name starts with "on", the bare
# word hx-on in prose, and the data-* replacements.
ui_near="$(fresh_ui_dir near)"
cat >"${ui_near}/partials/n.html" <<'EOF'
<script>var online = navigator.onLine; var only = 1;</script>
<p>set hx-on carefully</p>
<form data-on-after-request="reload" data-action="x"></form>
EOF
expect_pass "near-misses (var online =, hx-on in prose, data-on-*)" "${ui_near}" "${GOOD_JS}" "${EMPTY_ALLOW}"

ui_hxon="$(fresh_ui_dir hxon)"
cat >"${ui_hxon}/partials/f.html" <<'EOF'
<form hx-post="/x" hx-on::after-request="if (event.detail.successful) UT.reload('x')"></form>
EOF
expect_fail "an hx-on::after-request attribute" "${ui_hxon}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# Inline handler built inside a <script> string — still a real violation
# (setup.html/tills.html build pairing buttons this way).
ui_jsstr="$(fresh_ui_dir jsstr)"
cat >"${ui_jsstr}/partials/s.html" <<'EOF'
<script>
  out.innerHTML = '<button '
    + 'hx-on::before-request="check()" '
    + '>go</button>';
</script>
EOF
expect_fail "an hx-on attribute built inside a script string" "${ui_jsstr}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# A comment BEFORE the violation must not shift its reported line number,
# or allowlist entries would silently stop matching.
ui_lines="$(fresh_ui_dir lines)"
cat >"${ui_lines}/partials/l.html" <<'EOF'
<!--
  multi
  line
-->
<button onclick="go()">x</button>
EOF
expect_fail "a violation after a multi-line comment" "${ui_lines}" "${GOOD_JS}" "${EMPTY_ALLOW}"
if grep -qF 'partials/l.html:5' "${OUT}"; then
  echo "✓ comment stripping preserves line numbers"
else
  echo "❌ FAIL: expected partials/l.html:5 in guard output" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# Exact file:line allowlist: matching entry passes, a stale line does not.
allow_exact="${TMPDIR}/allow_exact.txt"
printf '# reviewed exception\npartials/l.html:5\n' >"${allow_exact}"
expect_pass "a violation listed in the allowlist by exact file:line" "${ui_lines}" "${GOOD_JS}" "${allow_exact}"
allow_stale="${TMPDIR}/allow_stale.txt"
printf 'partials/l.html:4\n' >"${allow_stale}"
expect_fail "a violation whose allowlist entry names a different line" "${ui_lines}" "${GOOD_JS}" "${allow_stale}"
allow_file="${TMPDIR}/allow_file.txt"
printf 'partials/l.html\n' >"${allow_file}"
expect_fail "a whole-file (no :line) allowlist entry" "${ui_lines}" "${GOOD_JS}" "${allow_file}"
expect_fail "a violation with no allowlist file at all (nothing exempt)" "${ui_lines}" "${GOOD_JS}" "${TMPDIR}/no_such_allowlist.txt"

# inline-actions.js with a delegation listener deleted, or only mentioned in
# a comment.
for ev in click error htmx:afterRequest htmx:beforeRequest; do
  js="${TMPDIR}/missing_${ev//:/_}.js"
  grep -vF "'${ev}'" "${GOOD_JS}" >"${js}"
  expect_fail "inline-actions.js missing its '${ev}' listener" "${ui_ok}" "${js}" "${EMPTY_ALLOW}"
done
commented_js="${TMPDIR}/commented.js"
cat >"${commented_js}" <<'EOF'
// document.addEventListener('click', ...)
// document.addEventListener('error', ..., true)
/* document.body.addEventListener('htmx:afterRequest', ...)
   document.body.addEventListener('htmx:beforeRequest', ...) */
(function () {})();
EOF
expect_fail "inline-actions.js whose listeners only appear in comments" "${ui_ok}" "${commented_js}" "${EMPTY_ALLOW}"
# error must be capture phase: it does not bubble.
bubble_js="${TMPDIR}/bubble.js"
sed "s/function () {}, true)/function () {})/" "${GOOD_JS}" >"${bubble_js}"
expect_fail "inline-actions.js with a non-capture 'error' listener" "${ui_ok}" "${bubble_js}" "${EMPTY_ALLOW}"

expect_fail "a nonexistent UI directory" "${TMPDIR}/nope" "${GOOD_JS}" "${EMPTY_ALLOW}"
expect_fail "a nonexistent inline-actions.js" "${ui_ok}" "${TMPDIR}/nope.js" "${EMPTY_ALLOW}"
empty_ui="${TMPDIR}/ui_empty"; mkdir -p "${empty_ui}"
expect_fail "a UI dir with no templates at all (fail-closed)" "${empty_ui}" "${GOOD_JS}" "${EMPTY_ALLOW}"

# The real tree must pass.
expect_pass "the real repo tree"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-no-inline-handlers_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-no-inline-handlers_test.sh: all assertions passed"
