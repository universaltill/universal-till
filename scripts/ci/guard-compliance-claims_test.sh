#!/usr/bin/env bash
#
# Regression test for guard-compliance-claims.sh (ut-docs#681): proves the
# guard catches each forbidden term from the product-owner-approved list
# (ut-docs#667, approved 2026-08-13), in a locale JSON value, a help-topic
# markdown file, and a UI template — case-insensitively, and catching the
# German forms specifically (that's where the real risk is: German fiscal
# marketing copy). Also proves a permitted phrase is never flagged, and that
# the same-line `compliance-claim:allow` escape hatch works.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-compliance-claims.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

fresh_dirs() {
  local tag="$1"
  local locales="${TMPDIR}/locales_${tag}"
  local help="${TMPDIR}/help_${tag}"
  local ui="${TMPDIR}/ui_${tag}"
  mkdir -p "${locales}" "${help}" "${ui}"
  printf '%s\n%s\n%s\n' "${locales}" "${help}" "${ui}"
}

expect_pass() {
  local label="$1" locales="$2" help="$3" ui="$4"
  if bash "${GUARD}" "${locales}" "${help}" "${ui}" >/tmp/guard_compliance_test_out.$$ 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat /tmp/guard_compliance_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_compliance_test_out.$$
}

expect_fail() {
  local label="$1" locales="$2" help="$3" ui="$4"
  if bash "${GUARD}" "${locales}" "${help}" "${ui}" >/tmp/guard_compliance_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_compliance_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_compliance_test_out.$$
}

# A clean, minimal fixture set using only permitted phrasing (the exact
# ut-docs#667-approved examples) — must pass.
read -r loc_ok help_ok ui_ok <<<"$(fresh_dirs ok | tr '\n' ' ')"
cat >"${loc_ok}/en.json" <<'EOF'
{
  "fiscal.tse.summary": "Signs every transaction using a BSI-certified technical security device (TSE).",
  "fiscal.tse.cloud": "Includes a cloud TSE — no separate hardware or contract needed.",
  "fiscal.export": "Exports DSFinV-K data for a cash audit.",
  "fiscal.notification_prep": "Prepares the information you need for your §146a Abs. 4 AO notification."
}
EOF
cat >"${help_ok}/fiscal.md" <<'EOF'
---
id: fiscal
title: Fiscal signing
---
Universal Till provides a certified TSE and the required exports. It does not
provide tax or legal advice, and your own record-keeping and reporting
obligations remain yours.
EOF
cat >"${ui_ok}/fiscal.html" <<'EOF'
<p>Works with a Swissbit hardware TSE or a TSE-equipped printer.</p>
EOF
expect_pass "a fixture set using only the approved permitted phrasing" "${loc_ok}" "${help_ok}" "${ui_ok}"

# Each forbidden term, planted one at a time in the locale file, must be
# caught — including the German forms, case-varied to prove insensitivity.
declare -a FORBIDDEN_CASES=(
  "GoBD-compliant"
  "GoBD-konform"
  "KassenSichV-compliant"
  "KassenSichV-konform"
  "FINANZAMTSKONFORM"
  "Audit-Proof"
  "revisionssicher"
  "Certified by the Finanzamt"
  "vom Finanzamt zertifiziert"
  "Approved by the Tax Office"
  "You Are Compliant"
  "fully compliant"
  "We file your §146a notification for you"
  "We submit your §146a notification for you"
  "We will file your §146a notification"
  "We handle your §146a filing so you don't have to"
  "We take care of your §146a for you"
  "Wir melden Ihre Kasse beim Finanzamt"
  "Wir reichen Ihre §146a Anmeldung ein"
  # The one term with a cased non-ASCII character (Ü) — pins the ut-docs#662-
  # class locale bug the review found: `grep -i` only folds Ü→ü in a UTF-8
  # locale, and this repo's own CI runner has no LANG set (the C locale) —
  # without guard-compliance-claims.sh forcing LC_ALL=C.UTF-8 itself, this
  # exact capitalized German heading would have shipped past CI undetected.
  "WIR ÜBERNEHMEN IHRE §146A-ANMELDUNG"
)
for term in "${FORBIDDEN_CASES[@]}"; do
  read -r loc help ui <<<"$(fresh_dirs "term_$(echo "$term" | tr -cd 'a-zA-Z0-9')" | tr '\n' ' ')"
  printf '{"x.claim": "%s"}\n' "$term" >"${loc}/en.json"
  # help/ui also need a real (permitted) file each — the per-surface
  # fail-closed check would otherwise reject this fixture for an empty
  # sibling dir, not for genuinely detecting the planted term.
  cp "${help_ok}"/*.md "${help}/"
  cp "${ui_ok}"/*.html "${ui}/"
  expect_fail "the forbidden term ${term@Q}" "${loc}" "${help}" "${ui}"
done

# The same forbidden term in a help topic and in a UI template must also be
# caught — the guard's three surfaces are independent, not just the locale
# path exercised above. Every fixture dir below carries a legitimate
# baseline file too (copied from the *_ok fixtures), so a per-surface
# fail-closed check (see above) can never make expect_fail pass for the
# wrong reason — vacuously, from an empty sibling dir, rather than genuinely
# detecting the planted term.
read -r loc_help help_help ui_help <<<"$(fresh_dirs help_surface | tr '\n' ' ')"
cp "${loc_ok}"/*.json "${loc_help}/"
cat >"${help_help}/fiscal.md" <<'EOF'
Our till is fully GoBD-compliant out of the box.
EOF
cp "${ui_ok}"/*.html "${ui_help}/"
expect_fail "a forbidden term in a help topic" "${loc_help}" "${help_help}" "${ui_help}"

read -r loc_ui help_ui ui_ui <<<"$(fresh_dirs ui_surface | tr '\n' ' ')"
cp "${loc_ok}"/*.json "${loc_ui}/"
cp "${help_ok}"/*.md "${help_ui}/"
echo '<p>revisionssicher, garantiert.</p>' >"${ui_ui}/fiscal.html"
expect_fail "a forbidden term in a UI template" "${loc_ui}" "${help_ui}" "${ui_ui}"

# compliance-claim:allow escape hatch — a reviewed exception (e.g. this very
# guard's own test data, or a doc explaining why a term is forbidden) must
# not fail the build when marked, in help/UI (locale JSON has no comment
# syntax and is NOT covered by this escape hatch — documented gap).
read -r loc_allow help_allow ui_allow <<<"$(fresh_dirs allow | tr '\n' ' ')"
cp "${loc_ok}"/*.json "${loc_allow}/"
cat >"${help_allow}/fiscal.md" <<'EOF'
We never claim "GoBD-compliant" anywhere in the product. <!-- compliance-claim:allow quoting the forbidden term to explain why we avoid it -->
EOF
cp "${ui_ok}"/*.html "${ui_allow}/"
expect_pass "a forbidden term with a same-line compliance-claim:allow marker" "${loc_allow}" "${help_allow}" "${ui_allow}"

# Missing directories entirely — fail closed, don't silently pass.
expect_fail "a nonexistent locales directory" "${TMPDIR}/does-not-exist" "${help_ok}" "${ui_ok}"

# Fail closed PER SURFACE (ut-docs#681 review finding): one surface going
# empty (renamed extension, moved tree) must fail on its own — not only when
# all three vanish together, which a combined counter would miss.
read -r loc_empty1 help_empty1 ui_empty1 <<<"$(fresh_dirs empty_locales | tr '\n' ' ')"
echo '{}' >"${loc_empty1}/en.json"
rm "${loc_empty1}/en.json"
cp "${help_ok}"/*.md "${help_empty1}/"
cp "${ui_ok}"/*.html "${ui_empty1}/"
expect_fail "an empty locales dir alone (help+ui populated)" "${loc_empty1}" "${help_empty1}" "${ui_empty1}"

read -r loc_empty2 help_empty2 ui_empty2 <<<"$(fresh_dirs empty_help | tr '\n' ' ')"
cp "${loc_ok}"/*.json "${loc_empty2}/"
cp "${ui_ok}"/*.html "${ui_empty2}/"
expect_fail "an empty help dir alone (locales+ui populated)" "${loc_empty2}" "${help_empty2}" "${ui_empty2}"

read -r loc_empty3 help_empty3 ui_empty3 <<<"$(fresh_dirs empty_ui | tr '\n' ' ')"
cp "${loc_ok}"/*.json "${loc_empty3}/"
cp "${help_ok}"/*.md "${help_empty3}/"
expect_fail "an empty ui dir alone (locales+help populated)" "${loc_empty3}" "${help_empty3}" "${ui_empty3}"

# The real, unmodified tree must still pass (no forbidden claims shipped yet).
expect_pass "the real repo tree" "web/locales" "web/help" "web/ui"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-compliance-claims_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-compliance-claims_test.sh: all assertions passed"
