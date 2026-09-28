#!/usr/bin/env bash
#
# Regression test for guard-no-showmodal.sh (ut-docs#2097): runs the guard
# against fixture ui/public trees in a fresh mktemp dir and proves it
# rejects a real .showModal() call (HTML attribute and JS), ignores every
# comment form (HTML, Go template, JS block across lines, // line and
# trailing), honours the self-order kiosk exemption only for
# #selforder-modal, honours `showmodal:allow`, skips web/public/vendor, and
# passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-no-showmodal.sh"
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
  printf '%s\n' '<dialog id="x" class="modifier-modal"></dialog>' >"${UI}/pages/base.html"
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
printf '%s\n' '<button onclick="document.getElementById('"'"'x'"'"').showModal()">x</button>' >"${UI}/partials/picker.html"
expect_fail "an HTML onclick .showModal() call"

reset_tree
printf '%s\n' 'function open() {' '  var m = document.getElementById("x");' '  if (m && !m.open) m.showModal ();' '}' >"${PUB}/app.js"
expect_fail "a JS .showModal () call in web/public"

reset_tree
printf '%s\n' 'HTMLDialogElement.prototype.showModal.call(m);' >"${PUB}/app.js"
expect_fail "a prototype .showModal.call() call"

reset_tree
printf '%s\n' 'm["showModal"]();' >"${PUB}/app.js"
expect_fail "a bracket-access showModal call"

reset_tree
printf '%s\n' 'var m = x; m.show(); m.showModal(); // was showModal' >"${PUB}/app.js"
expect_fail "a call followed by a trailing comment"

reset_tree
cat >"${UI}/partials/comments.html" <<'EOF'
<!-- ut-docs#2097: opened with .show(), never .showModal() --
     a showModal() dialog makes the status bar inert; not
     m.showModal() either. -->
{{/* same for x.showModal() in a
     template comment */}}
<script>
  // never d.showModal() here
  /* nor d.showModal()
     across d.showModal() lines */
  d.show(); // not d.showModal()
</script>
EOF
expect_pass "every comment form"

reset_tree
printf '%s\n' '<button hx-on::after-request="document.getElementById('"'"'selforder-modal'"'"').showModal()">x</button>' >"${UI}/partials/self_order_grid.html"
printf '%s\n' '<button hx-on::after-request="if (ok) { document.getElementById('"'"'selforder-modal'"'"').showModal(); }">x</button>' >"${UI}/partials/self_order_cart.html"
expect_pass "the self-order kiosk opening #selforder-modal"

reset_tree
printf '%s\n' '<button onclick="document.getElementById('"'"'table-modal'"'"').showModal()">x</button>' >"${UI}/partials/self_order_grid.html"
expect_fail "a self-order file opening a NON-kiosk dialog modally"

reset_tree
printf '%s\n' '<button onclick="document.getElementById('"'"'selforder-modal'"'"').showModal()">x</button>' >"${UI}/partials/till_picker.html"
expect_fail "#selforder-modal opened modally outside the kiosk files"

reset_tree
printf '%s\n' 'd.showModal(); // showmodal:allow reviewed: fixture' >"${PUB}/app.js"
expect_pass "a showmodal:allow marker"

reset_tree
printf '%s\n' 'd.showModal();' >"${PUB}/vendor/lib.js"
expect_pass "web/public/vendor"

reset_tree
printf '%s\n' 'd.showModal();' >"${PUB}/app.css"
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
  echo "❌ ${FAIL_COUNT} guard-no-showmodal test(s) failed" >&2
  exit 1
fi
echo "✓ guard-no-showmodal: all tests passed"
