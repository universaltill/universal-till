#!/usr/bin/env bash
#
# Regression test for guard-competitor-naming.sh (ADR-0106 Decision D,
# ut-docs#1905): proves the guard catches competitor-imitation phrasings in
# a locale JSON value, a help topic, a UI template and a plugin manifest —
# case-insensitively, including the German form — and catches a BARE
# competitor name in a layout/theme plugin's manifest and its own locale
# file. Also proves what must stay allowed: a plain factual mention of a
# competitor in core copy (import formats, a payment provider), a payment
# plugin naming the provider it integrates, and the same-line
# `naming-rule:allow` escape hatch in help/UI (and NOT in JSON).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-competitor-naming.sh"
FAIL_COUNT=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

fresh_dirs() {
  local tag="$1"
  local locales="${TMPDIR}/locales_${tag}"
  local help="${TMPDIR}/help_${tag}"
  local ui="${TMPDIR}/ui_${tag}"
  local plugins="${TMPDIR}/plugins_${tag}"
  mkdir -p "${locales}" "${help}" "${ui}" "${plugins}"
  printf '%s\n%s\n%s\n%s\n' "${locales}" "${help}" "${ui}" "${plugins}"
}

expect_pass() {
  local label="$1" locales="$2" help="$3" ui="$4" plugins="$5"
  if bash "${GUARD}" "${locales}" "${help}" "${ui}" "${plugins}" >"${TMPDIR}/out.$$" 2>&1; then
    echo "✓ guard correctly passed ${label}"
  else
    echo "❌ FAIL: expected guard to pass ${label}, but it rejected it" >&2
    cat "${TMPDIR}/out.$$" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f "${TMPDIR}/out.$$"
}

expect_fail() {
  local label="$1" locales="$2" help="$3" ui="$4" plugins="$5"
  if bash "${GUARD}" "${locales}" "${help}" "${ui}" "${plugins}" >"${TMPDIR}/out.$$" 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat "${TMPDIR}/out.$$" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f "${TMPDIR}/out.$$"
}

# copy_ok DEST_LOCALES DEST_HELP DEST_UI DEST_PLUGINS — every fixture below
# starts from the clean baseline so the per-surface fail-closed check can
# never make expect_fail pass for the wrong reason (an empty sibling dir
# rather than the planted term).
copy_ok() {
  cp "${loc_ok}"/*.json "$1/"
  cp "${help_ok}"/*.md "$2/"
  cp "${ui_ok}"/*.html "$3/"
  cp -r "${plugins_ok}"/. "$4/"
}

# The clean baseline: factual competitor MENTIONS that must stay allowed
# (they are real lines in this repo's copy), a layout plugin named for what
# it does, and a payment plugin naming the provider it integrates.
read -r loc_ok help_ok ui_ok plugins_ok <<<"$(fresh_dirs ok | tr '\n' ' ')"
cat >"${loc_ok}/en.json" <<'EOF'
{
  "import.help": "Bring your items over from another till: Loyverse, Square and SumUp exports are recognised automatically.",
  "layout.compact.name": "Compact layout"
}
EOF
cat >"${help_ok}/payments.md" <<'EOF'
---
id: payments
title: Payments
---
Install a payment plugin from the store — Stripe with a card reader, SumUp,
a QR-code payment provider, and others all install the same way. Square
tiles show the toast notification in the corner.
EOF
cat >"${ui_ok}/index.html" <<'EOF'
<p>{{ T "layout.compact.name" }}</p>
<div class="toast">{{ T "sale.saved" }}</div>
EOF
mkdir -p "${plugins_ok}/layout-compact/locales" "${plugins_ok}/payment-cardreader"
cat >"${plugins_ok}/layout-compact/plugin.json" <<'EOF'
{
  "id": "com.example.layout-compact",
  "name": "Compact layout",
  "description": "A denser Menu for small screens: fewer tiles, the same destinations.",
  "canonical_type": "layout",
  "entries": [{ "type": "layout", "key": "menu", "label": "layout.compact.name", "config": { "slot": "menu", "role": "preset", "amendments": [] } }]
}
EOF
echo '{ "layout.compact.name": "Compact" }' >"${plugins_ok}/layout-compact/locales/en.json"
cat >"${plugins_ok}/payment-cardreader/plugin.json" <<'EOF'
{
  "id": "com.example.payment-cardreader",
  "name": "Card reader",
  "description": "Takes card payments through a SumUp card reader.",
  "canonical_type": "payment",
  "entries": []
}
EOF
expect_pass "a fixture set with factual competitor mentions and neutrally named plugins" "${loc_ok}" "${help_ok}" "${ui_ok}" "${plugins_ok}"

# Imitation phrasings, planted one at a time in the locale file — every
# name on the list in at least one shape, case-varied, the German form, the
# product forms of the two ambiguous names, and the hyphenated forms.
declare -a IMITATION_CASES=(
  "SumUp-style tiles"
  "A SumUp look for your register"
  "Like Zettle, but yours"
  "iZettle mode"
  "wie ready2order"
  "Inspired by Lightspeed"
  "Shopify preset"
  "The Shopify POS layout"
  "Square POS style"
  "SquareUp-like"
  "Square-style tiles"
  "Modelled on ToastTab"
  "Toast-style ordering"
  "Familiar from Toast POS"
  "A Lightspeed clone"
)
for term in "${IMITATION_CASES[@]}"; do
  read -r loc help ui plugins <<<"$(fresh_dirs "term_$(echo "$term" | tr -cd 'a-zA-Z0-9')" | tr '\n' ' ')"
  copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
  printf '{"x.claim": "%s"}\n' "$term" >"${loc}/en.json"
  expect_fail "the imitation phrase ${term@Q} in a locale file" "${loc}" "${help}" "${ui}" "${plugins}"
done

# The same phrase in each of the other three surfaces must also be caught.
read -r loc help ui plugins <<<"$(fresh_dirs help_surface | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
echo 'The Menu is laid out SumUp-style, so it feels familiar.' >"${help}/layout.md"
expect_fail "an imitation phrase in a help topic" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs ui_surface | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
echo '<p>Just like SumUp.</p>' >"${ui}/layout.html"
expect_fail "an imitation phrase in a UI template" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs manifest_surface | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
sed -i 's/Takes card payments through a SumUp card reader./A Zettle-like checkout./' "${plugins}/payment-cardreader/plugin.json"
expect_fail "an imitation phrase in a non-presentation plugin manifest" "${loc}" "${help}" "${ui}" "${plugins}"

# Tier 2: a BARE competitor name in a layout or theme plugin's manifest, or
# in that plugin's own locale file, is a violation on its own.
read -r loc help ui plugins <<<"$(fresh_dirs bare_layout_name | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
sed -i 's/"name": "Compact layout"/"name": "SumUp Classic"/' "${plugins}/layout-compact/plugin.json"
expect_fail "a bare competitor name as a layout plugin's name" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs bare_layout_locale | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
echo '{ "layout.compact.name": "Lightspeed" }' >"${plugins}/layout-compact/locales/en.json"
expect_fail "a bare competitor name in a layout plugin's own locale file" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs bare_theme | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
mkdir -p "${plugins}/theme-x"
cat >"${plugins}/theme-x/plugin.json" <<'EOF'
{ "id": "com.example.theme-x", "name": "Midnight", "description": "The Square POS palette, dark.", "canonical_type": "theme" }
EOF
expect_fail "a bare product name in a theme plugin's description" "${loc}" "${help}" "${ui}" "${plugins}"

# ...but "square" and "toast" as ordinary words in a theme manifest are fine.
read -r loc help ui plugins <<<"$(fresh_dirs plain_words | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
mkdir -p "${plugins}/theme-y"
cat >"${plugins}/theme-y/plugin.json" <<'EOF'
{ "id": "com.example.theme-y", "name": "Squared", "description": "Square corners, a toast in the corner, and a warm palette.", "canonical_type": "theme" }
EOF
expect_pass "square/toast used as ordinary words in a theme manifest" "${loc}" "${help}" "${ui}" "${plugins}"

# naming-rule:allow escape hatch — a reviewed exception in help/UI passes;
# the same marker in locale JSON or a plugin manifest does NOT (plain JSON
# has no comment syntax; documented gap, same as the compliance guard).
read -r loc help ui plugins <<<"$(fresh_dirs allow_help | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
echo 'We never describe a preset as "SumUp-style". <!-- naming-rule:allow quoting the forbidden phrase to explain the rule -->' >"${help}/layout.md"
expect_pass "an imitation phrase with a same-line naming-rule:allow marker in help" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs allow_ui | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
echo '<!-- product-owner feedback, quoted: "make it like sumup" — naming-rule:allow -->' >"${ui}/layout.html"
expect_pass "an imitation phrase with a same-line naming-rule:allow marker in a template" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs allow_locale | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
echo '{"x.claim": "SumUp-style naming-rule:allow"}' >"${loc}/en.json"
expect_fail "a naming-rule:allow marker in locale JSON (no escape hatch there)" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs allow_manifest | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
sed -i 's/"name": "Compact layout"/"name": "SumUp naming-rule:allow"/' "${plugins}/layout-compact/plugin.json"
expect_fail "a naming-rule:allow marker in a plugin manifest (no escape hatch there)" "${loc}" "${help}" "${ui}" "${plugins}"

# Missing directory — fail closed.
expect_fail "a nonexistent plugins directory" "${loc_ok}" "${help_ok}" "${ui_ok}" "${TMPDIR}/does-not-exist"

# Fail closed PER SURFACE: any one surface going empty fails on its own.
read -r loc help ui plugins <<<"$(fresh_dirs empty_locales | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
rm "${loc}"/*.json
expect_fail "an empty locales dir alone" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs empty_help | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
rm "${help}"/*.md
expect_fail "an empty help dir alone" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs empty_ui | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
rm "${ui}"/*.html
expect_fail "an empty ui dir alone" "${loc}" "${help}" "${ui}" "${plugins}"

read -r loc help ui plugins <<<"$(fresh_dirs empty_plugins | tr '\n' ' ')"
copy_ok "${loc}" "${help}" "${ui}" "${plugins}"
rm "${plugins}"/*/plugin.json
expect_fail "a plugins dir with no plugin.json alone" "${loc}" "${help}" "${ui}" "${plugins}"

# The real, unmodified tree must pass (the two template comments that quote
# the product owner carry the reviewed naming-rule:allow marker).
expect_pass "the real repo tree" "web/locales" "web/help" "web/ui" "plugins"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard-competitor-naming_test.sh assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-competitor-naming_test.sh: all assertions passed"
