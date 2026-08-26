#!/usr/bin/env bash
#
# Regression test for guard-osk-loaded.sh (ut-docs#1096): proves the guard
# rejects a standalone document with a text-like input that doesn't load
# osk.js, PASSES a standalone document with no text-like input even without
# osk.js (the input-aware difference from guard-autofill-suppression.sh),
# ignores a partial (no <html>), rejects a commented-out script tag, rejects
# osk.js with its own machinery removed, and passes both a minimal good
# fixture set and the real, unmodified repo tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-osk-loaded.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

GOOD_JS="${TMPDIR}/osk.js"
cat >"${GOOD_JS}" <<'EOF'
(function () {
  var LAYOUTS = { en: [] };
  function wantsOSK(el) { return true; }
  function guardSweep(root) {}
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
  if bash "${GUARD}" "${ui}" "${js}" >/tmp/guard_osk_test_out.$$ 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat /tmp/guard_osk_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_osk_test_out.$$
}

expect_fail() {
  local label="$1" ui="$2" js="$3"
  if bash "${GUARD}" "${ui}" "${js}" >/tmp/guard_osk_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_osk_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_osk_test_out.$$
}

# A minimal set of standalone documents: one with a texty input that loads
# osk.js, one with NO texty input and no osk.js (must still pass — this is
# the input-aware difference from guard-autofill-suppression.sh), and a
# partial (no <html>) — must all pass.
ui_ok="$(fresh_ui_dir ok)"
cat >"${ui_ok}/login.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/osk.js?v=1"></script></head>
<body><input type="password" name="pin"></body></html>
EOF
cat >"${ui_ok}/self_order.html" <<'EOF'
<!DOCTYPE html>
<html><head></head><body><button type="button">Order</button></body></html>
EOF
cat >"${ui_ok}/partial.html" <<'EOF'
{{ define "content" }}<input type="text" name="code">{{ end }}
EOF
expect_pass "a texty-input doc loading osk.js plus a keyboard-free doc without it" "${ui_ok}" "${GOOD_JS}"

# The exact ut-docs#1096 finding: a standalone document with text inputs
# that never loads osk.js.
ui_missing="$(fresh_ui_dir missing)"
cat >"${ui_missing}/setup.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/vendor/htmx.min.js"></script></head>
<body><input type="text" name="store_name"></body></html>
EOF
expect_fail "a standalone document with a text input that never loads osk.js" "${ui_missing}" "${GOOD_JS}"

# An <input> with no type= attribute at all still counts (HTML default is
# text) — a guard that only matched an explicit type="text" would miss it.
ui_untyped="$(fresh_ui_dir untyped)"
cat >"${ui_untyped}/untyped.html" <<'EOF'
<!DOCTYPE html>
<html><head></head><body><input name="freeform" placeholder="type here"></body></html>
EOF
expect_fail "a standalone document with an untyped (default-text) input and no osk.js" "${ui_untyped}" "${GOOD_JS}"

# A <textarea> counts too.
ui_textarea="$(fresh_ui_dir textarea)"
cat >"${ui_textarea}/notes.html" <<'EOF'
<!DOCTYPE html>
<html><head></head><body><textarea name="notes"></textarea></body></html>
EOF
expect_fail "a standalone document with a textarea and no osk.js" "${ui_textarea}" "${GOOD_JS}"

# Only non-texty inputs (checkbox/radio/hidden/button) — must pass WITHOUT
# osk.js, unlike the blanket autofill guard. This is what keeps
# self_order.html/self_order_shop.html/order_tracking.html green today.
ui_nontexty="$(fresh_ui_dir nontexty)"
cat >"${ui_nontexty}/toggle_only.html" <<'EOF'
<!DOCTYPE html>
<html><head></head><body>
  <input type="checkbox" name="agree">
  <input type="radio" name="choice">
  <input type="hidden" name="csrf">
  <input type="submit" value="Go">
  <button type="button">Tap</button>
</body></html>
EOF
expect_pass "a standalone document with only non-texty inputs, no osk.js needed" "${ui_nontexty}" "${GOOD_JS}"

# A MULTI-LINE <input> tag whose type= sits on a later line than its own
# `<input`. This is the repo's prevailing house style (19 templates under
# web/ui/ format inputs this way), and a line-oriented grep cannot see
# across the newline — the guard's explicit-type probe used to be exactly
# that, so a page written in the repo's own style fell through both the
# texty probe AND the untyped probe (which correctly saw the type= and so
# declined to call it untyped) and got a free pass. Found in review of
# ut-docs#1096.
ui_multiline="$(fresh_ui_dir multiline)"
cat >"${ui_multiline}/wizard.html" <<'EOF'
<!DOCTYPE html>
<html><head></head><body>
  <input
    type="text"
    name="store_name"
    placeholder="Shop name" />
</body></html>
EOF
expect_fail "a standalone document whose texty input's type= is on a later line, no osk.js" "${ui_multiline}" "${GOOD_JS}"

# A LAYOUT is required to load osk.js even with no <input> of its own:
# web/ui/layouts/base.html has none (every field lives in the page
# templates and partials it composes in), so the input-aware rule alone
# would leave the app's single most important document unguarded —
# deleting its osk.js <script> would take the keyboard off every
# base-layout page at once while this guard stayed green. Found in review.
ui_layout="$(fresh_ui_dir layout)"
mkdir -p "${ui_layout}/layouts" "${ui_layout}/partials"
cat >"${ui_layout}/layouts/base.html" <<'EOF'
<!DOCTYPE html>
<html><head></head><body>{{ template "content" . }}</body></html>
EOF
cat >"${ui_layout}/partials/form.html" <<'EOF'
{{ define "content" }}<input type="text" name="q">{{ end }}
EOF
expect_fail "a layout with no input of its own that never loads osk.js" "${ui_layout}" "${GOOD_JS}"

# ...and the same layout passes once it loads osk.js, input-free as it is.
ui_layout_ok="$(fresh_ui_dir layout_ok)"
mkdir -p "${ui_layout_ok}/layouts"
cat >"${ui_layout_ok}/layouts/base.html" <<'EOF'
<!DOCTYPE html>
<html><head><script defer src="/public/osk.js?v=1"></script></head>
<body>{{ template "content" . }}</body></html>
EOF
expect_pass "an input-free layout that does load osk.js" "${ui_layout_ok}" "${GOOD_JS}"

# A standalone document whose osk.js script tag is commented out —
# guard-htmx-loaded.sh's own regression class.
ui_commented="$(fresh_ui_dir commented)"
cat >"${ui_commented}/setup.html" <<'EOF'
<!DOCTYPE html>
<html><head><!-- <script defer src="/public/osk.js?v=1"></script> --></head>
<body><input type="text" name="x"></body></html>
EOF
expect_fail "a standalone document with osk.js commented out" "${ui_commented}" "${GOOD_JS}"

# No standalone document at all — fail closed, don't silently pass.
ui_empty="$(fresh_ui_dir empty)"
cat >"${ui_empty}/partial_only.html" <<'EOF'
{{ define "content" }}<p>no html tag here</p>{{ end }}
EOF
expect_fail "a UI dir with no standalone document at all (fail-closed)" "${ui_empty}" "${GOOD_JS}"

# osk.js with its own machinery stripped out.
stripped_js="${TMPDIR}/osk_stripped.js"
echo '(function () { /* nothing here */ })();' >"${stripped_js}"
expect_fail "osk.js with its keyboard machinery removed" "${ui_ok}" "${stripped_js}"

# osk.js whose only mention of a required marker is inside a comment.
commented_marker_js="${TMPDIR}/osk_commented_marker.js"
cat >"${commented_marker_js}" <<'EOF'
// This used to use wantsOSK and guardSweep and MutationObserver and LAYOUTS,
// but that was all ripped out.
(function () {})();
EOF
expect_fail "osk.js whose markers only appear inside comments" "${ui_ok}" "${commented_marker_js}"

# Missing files entirely.
expect_fail "a nonexistent UI directory" "${TMPDIR}/does-not-exist" "${GOOD_JS}"
expect_fail "a nonexistent osk.js path" "${ui_ok}" "${TMPDIR}/does-not-exist.js"

# The real, unmodified (post-fix) tree must pass.
expect_pass "the real repo tree" "web/ui" "web/public/osk.js"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-osk-loaded_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-osk-loaded_test.sh: all assertions passed"
