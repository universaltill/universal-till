#!/usr/bin/env bash
#
# Regression test for guard-autofill-suppression.sh (ut-docs#400): proves the
# guard rejects a standalone document that doesn't load autofill.js, ignores
# a partial (no <html>), rejects a commented-out script tag, rejects
# autofill.js with the sweep's machinery removed (in real code, not just in
# a comment), and passes both a minimal good fixture set and the real,
# unmodified repo tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-autofill-suppression.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

GOOD_JS="${TMPDIR}/autofill.js"
cat >"${GOOD_JS}" <<'EOF'
(function () {
  var TEXTY_TYPES = { text: 1 };
  function suppress(el) {
    if (el.hasAttribute('data-allow-autofill')) return;
  }
  document.addEventListener('htmx:afterSwap', function () {});
  new MutationObserver(function () {}).observe(document.documentElement, {});
})();
EOF

fresh_ui_dir() {
  local dir="${TMPDIR}/ui_$1"
  mkdir -p "${dir}"
  printf '%s' "${dir}"
}

expect_pass() {
  local label="$1" ui="$2" js="$3"
  if bash "${GUARD}" "${ui}" "${js}" >/tmp/guard_autofill_test_out.$$ 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat /tmp/guard_autofill_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_autofill_test_out.$$
}

expect_fail() {
  local label="$1" ui="$2" js="$3"
  if bash "${GUARD}" "${ui}" "${js}" >/tmp/guard_autofill_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_autofill_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_autofill_test_out.$$
}

# A minimal set of standalone documents that all load autofill.js, plus a
# partial (no <html>) that doesn't — must still pass, since partials inherit
# from whatever layout they're swapped into.
ui_ok="$(fresh_ui_dir ok)"
cat >"${ui_ok}/base.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/autofill.js?v=1"></script></head><body></body></html>
EOF
cat >"${ui_ok}/login.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/autofill.js?v=1"></script></head><body></body></html>
EOF
cat >"${ui_ok}/partial.html" <<'EOF'
{{ define "content" }}<input type="text" name="code">{{ end }}
EOF
expect_pass "a minimal set of standalone documents all loading autofill.js" "${ui_ok}" "${GOOD_JS}"

# A standalone document that never loads autofill.js at all — the exact
# ut-docs#400 review finding (login.html/setup.html before the fix).
ui_missing="$(fresh_ui_dir missing)"
cp "${ui_ok}/base.html" "${ui_missing}/base.html"
cat >"${ui_missing}/setup.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/vendor/htmx.min.js"></script></head><body></body></html>
EOF
expect_fail "a standalone document that never loads autofill.js" "${ui_missing}" "${GOOD_JS}"

# A standalone document whose autofill.js script tag is commented out —
# guard-htmx-loaded.sh's own regression class, reintroduced here if the
# comment-stripping step is ever dropped.
ui_commented="$(fresh_ui_dir commented)"
cat >"${ui_commented}/setup.html" <<'EOF'
<!DOCTYPE html>
<html><head><!-- <script defer src="/public/autofill.js?v=1"></script> --></head><body></body></html>
EOF
expect_fail "a standalone document with autofill.js commented out" "${ui_commented}" "${GOOD_JS}"

# No standalone document at all — fail closed, don't silently pass.
ui_empty="$(fresh_ui_dir empty)"
cat >"${ui_empty}/partial_only.html" <<'EOF'
{{ define "content" }}<p>no html tag here</p>{{ end }}
EOF
expect_fail "a UI dir with no standalone document at all (fail-closed)" "${ui_empty}" "${GOOD_JS}"

# autofill.js with the sweep's own machinery stripped out.
stripped_js="${TMPDIR}/autofill_stripped.js"
echo '(function () { /* nothing here */ })();' >"${stripped_js}"
expect_fail "autofill.js with the suppression sweep removed" "${ui_ok}" "${stripped_js}"

# autofill.js whose only mention of a required marker is inside a comment —
# must not count, same reasoning as the commented script-tag case above.
commented_marker_js="${TMPDIR}/autofill_commented_marker.js"
cat >"${commented_marker_js}" <<'EOF'
// This used to use a MutationObserver and TEXTY_TYPES and data-allow-autofill
// and htmx:afterSwap, but that was all ripped out.
(function () {})();
EOF
expect_fail "autofill.js whose markers only appear inside comments" "${ui_ok}" "${commented_marker_js}"

# Missing files entirely.
expect_fail "a nonexistent UI directory" "${TMPDIR}/does-not-exist" "${GOOD_JS}"
expect_fail "a nonexistent autofill.js path" "${ui_ok}" "${TMPDIR}/does-not-exist.js"

# The real, unmodified tree must still pass.
expect_pass "the real repo tree" "web/ui" "web/public/autofill.js"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-autofill-suppression_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-autofill-suppression_test.sh: all assertions passed"
