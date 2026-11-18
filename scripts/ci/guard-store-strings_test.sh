#!/usr/bin/env bash
#
# Regression test for guard-store-strings.sh (ut-docs#6401): fixture trees in
# a fresh mktemp dir prove it fails a checkout/plans URL, a plan price and
# buy/subscribe/upgrade/renew wording in help, release notes and locale
# strings; ignores the same text inside a <!-- plan-purchase --> block, an
# exempted key and a `store-strings:allow` line; fails a stale exempt entry;
# and passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-store-strings.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

REPO="${WORK}/repo"

reset_tree() {
  rm -rf -- "${REPO:?}"
  mkdir -p "${REPO}/web/help/en" "${REPO}/web/help/de" "${REPO}/web/release-notes/en" "${REPO}/web/locales" "${REPO}/web/ui/pages" "${REPO}/web/public" "${REPO}/scripts/ci"
  printf '# Claim\n\nYour claim code is shown as text.\n' >"${REPO}/web/help/en/claim.md"
  printf -- '---\nversion: v1.0.0\ndate: 2026-01-01\n---\n- A fix.\n' >"${REPO}/web/release-notes/en/v1.0.0.md"
  printf '{"a.b": "Your claim code:"}\n' >"${REPO}/web/locales/en.json"
  printf '{"a.b": "Ihr Anspruchscode:"}\n' >"${REPO}/web/locales/de.json"
  printf '# Anspruch\n\nDer Code wird als Text gezeigt.\n' >"${REPO}/web/help/de/claim.md"
  printf '{{ define "content" }}<p>{{ T "a.b" }}</p>{{ end }}\n' >"${REPO}/web/ui/pages/x.html"
  printf 'const a = 1; // Buy now\n' >"${REPO}/web/public/x.js"
  : >"${REPO}/scripts/ci/store-strings-exempt.txt"
}

# add_help appends one line to the English claim topic; add_tpl overwrites the template.
add_help() { printf '\n%s\n' "$1" >>"${REPO}/web/help/en/claim.md"; }
add_tpl() { printf '%s\n' "$1" >"${REPO}/web/ui/pages/x.html"; }

expect() {
  local want="$1" what="$2" got
  if bash "${GUARD}" "${REPO}" >"${WORK}/out" 2>&1; then got=pass; else got=fail; fi
  if [ "$got" = "$want" ]; then
    echo "ok guard -> ${want}: ${what}"
  else
    echo "FAIL guard -> ${got}, want ${want}: ${what}"
    cat "${WORK}/out"
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

reset_tree
expect pass "clean fixture"

for bad in \
  "Open https://my.example.test/plans to pick one." \
  "Visit https://example.test/checkout now." \
  "Plans start from £5 a month." \
  "Plans cost 9 EUR, or €10 per month." \
  "Buy a plan on the website." \
  "You can subscribe to the cloud plan." \
  "Upgrade your plan any time." \
  "Renew your subscription online." \
  "Choose a plan and scan the QR."; do
  reset_tree
  printf '\n%s\n' "$bad" >>"${REPO}/web/help/en/claim.md"
  expect fail "help: ${bad}"
done

reset_tree
printf '\n<!-- plan-purchase -->\n\nBuy a plan on the website: https://example.test/plans\n\n<!-- /plan-purchase -->\n' >>"${REPO}/web/help/en/claim.md"
printf '\n<!-- plan-purchase -->\n\nx\n\n<!-- /plan-purchase -->\n' >>"${REPO}/web/help/de/claim.md" # translations carry the markers too
expect pass "help: same text inside a plan-purchase block"

reset_tree
printf '\nBuy a plan. store-strings:allow reviewed by legal\n' >>"${REPO}/web/help/en/claim.md"
expect pass "help: store-strings:allow line"

reset_tree
printf -- '- Renew or change your plan from the till.\n' >>"${REPO}/web/release-notes/en/v1.0.0.md"
expect fail "release notes: renew wording"

reset_tree
printf '{"a.b": "Subscribe to the cloud plan online."}\n' >"${REPO}/web/locales/en.json"
expect fail "locale: subscribe wording"

reset_tree
printf '{"a.b": "Subscribe to the cloud plan online."}\n' >"${REPO}/web/locales/en.json"
printf 'a.b  # not on store builds\n' >"${REPO}/scripts/ci/store-strings-exempt.txt"
expect pass "locale: exempted key"

reset_tree
printf 'gone.key  # stale\n' >"${REPO}/scripts/ci/store-strings-exempt.txt"
expect fail "stale exempt entry"

# --- review M3: realistic steering text the first version let through ---
for bad in \
  "Upgrade now" \
  "Upgrade to Pro today." \
  "Buy now at universaltill.com/pricing" \
  "Go to universaltill.com/plans" \
  "Visit my.universaltill.com/billing" \
  "Open https://my.universaltill.com/stores/x/plan" \
  "Get a paid plan on our website." \
  "Unlock paid features on our website" \
  "Renew it in your account" \
  "Scan with your phone to renew" \
  "Paid plans start at £9.99." \
  "Just £9.99 monthly" \
  "9.99 GBP per month" \
  "Pro costs €12 a month"; do
  reset_tree
  add_help "$bad"
  expect fail "help: ${bad}"
  reset_tree
  printf '{"a.b": "%s"}\n' "$bad" >"${REPO}/web/locales/en.json"
  expect fail "locale: ${bad}"
done

reset_tree
add_help "A £2 discount applies to the item. Your plan is paid until 1 May."
expect pass "help: an item price and plan STATUS wording are allowed"

# --- block markers must match the Go parsers exactly ---
reset_tree
add_help "Some text <!-- plan-purchase -->"
add_help "Buy a plan on the website."
add_help "<!-- /plan-purchase -->"
expect fail "marker at the end of a text line is not a marker (text stays scanned)"

reset_tree
add_help "<!-- plan-purchase -->"
add_help "Buy a plan on the website."
expect fail "unclosed marker"

reset_tree
add_help "<!--plan-purchase-->"
expect fail "malformed marker (the app refuses it)"

reset_tree
add_help "<!-- plan-purchase -->"
add_help "<!-- plan-purchase -->"
add_help "<!-- /plan-purchase -->"
expect fail "nested marker"

reset_tree
add_help "<!-- /plan-purchase -->"
expect fail "closer without an opener"

reset_tree
add_help "   <!-- plan-purchase -->   "
add_help "Buy a plan on the website."
add_help "  <!-- /plan-purchase -->"
printf '\n<!-- plan-purchase -->\n\nx\n\n<!-- /plan-purchase -->\n' >>"${REPO}/web/help/de/claim.md"
expect pass "indented markers alone on their line (trimmed, as the app does)"

# --- templates and scripts ---
reset_tree
add_tpl '<p>Upgrade now to unlock cloud sync.</p>'
expect fail "template: hard-coded steering text"

reset_tree
add_tpl '{{ if planpurchaseallowed }}<p>Upgrade now to unlock cloud sync.</p>{{ end }}'
expect pass "template: inside {{ if planpurchaseallowed }}"

reset_tree
add_tpl '{{ if planpurchaseallowed }}{{ if x }}<p>Buy now</p>{{ end }}{{ else }}<p>Buy now</p>{{ end }}'
expect fail "template: the else branch of the gate is rendered on a store build"

reset_tree
add_tpl '{{ if not planpurchaseallowed }}<p>Buy now</p>{{ end }}'
expect fail "template: the body of {{ if not planpurchaseallowed }} is rendered on a store build"

reset_tree
add_tpl '{{/* Buy now */}}<!-- Upgrade now --><p class="subscription-renew" data-testid="plan-buy">ok</p>'
expect pass "template: comments, class names and test ids are not text"

reset_tree
add_tpl '<button title="Buy now at universaltill.com/pricing">x</button>'
expect fail "template: text-bearing attribute"

reset_tree
printf 'el.textContent = "Upgrade now";\n' >"${REPO}/web/public/x.js"
expect fail "script: hard-coded steering string"

# --- translations ---
reset_tree
printf '\nBuchen Sie unter https://example.test/plans\n' >>"${REPO}/web/help/de/claim.md"
expect fail "translated help: checkout URL"

reset_tree
printf '\nTarife ab 9,99 GBP pro Monat\n' >>"${REPO}/web/help/de/claim.md"
expect fail "translated help: price"

reset_tree
printf '{"a.b": "Nur 9,99 EUR pro Monat"}\n' >"${REPO}/web/locales/de.json"
expect fail "translated locale: price"

reset_tree
printf '\n<!-- plan-purchase -->\n\nBlock\n\n<!-- /plan-purchase -->\n' >>"${REPO}/web/help/en/claim.md"
expect fail "translation with fewer plan-purchase blocks than the English topic"
printf '\n<!-- plan-purchase -->\n\nBlock\n\n<!-- /plan-purchase -->\n' >>"${REPO}/web/help/de/claim.md"
expect pass "translation carrying the same markers"

# --- shrink-only exempt list ---
reset_tree
printf 'a.b  # clean text: the exemption is not needed\n' >"${REPO}/scripts/ci/store-strings-exempt.txt"
expect fail "exempt key whose text is clean (no longer needed)"

reset_tree
: >"${REPO}/scripts/ci/store-strings-exempt.txt"
{
  printf '{'
  for i in $(seq 1 12); do printf '"k%s": "Upgrade now",' "$i"; done
  printf '"z": "x"}\n'
} >"${REPO}/web/locales/en.json"
for i in $(seq 1 12); do printf 'k%s  # seeded\n' "$i" >>"${REPO}/scripts/ci/store-strings-exempt.txt"; done
expect fail "exempt list above the shrink-only ceiling"

reset_tree
rm -rf "${REPO}/web/help"
expect fail "no help topics (broken scan)"

if bash "${GUARD}" "${ROOT_DIR}" >"${WORK}/out" 2>&1; then
  echo "ok guard -> pass: real tree"
else
  echo "FAIL guard on the real tree:"
  cat "${WORK}/out"
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

[ "${FAIL_COUNT}" -eq 0 ]
