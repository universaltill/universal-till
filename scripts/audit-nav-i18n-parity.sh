#!/usr/bin/env bash
#
# Advisory audit (ut-docs#1848): flags a locale's top-level menu label
# (nav.*) or matching screen title that is byte-identical to the English
# source — key-complete but semantically untranslated, e.g. German's
# nav.designer/nav.journal both shipping the value "Designer"/"Journal"
# unchanged. guard-i18n.sh only checks KEY parity (every locale has the
# same keys), never VALUE content — an identical string passes that check
# cleanly, which is exactly how this shipped unnoticed.
#
# NOT wired into ci.yml's build job and never will be: a loanword (GitHub,
# Bon, Plugins in German) legitimately matches English, so an identical
# value is not automatically a bug — a human has to look. This script
# produces a report to read, not a gate to satisfy. Run it by hand after
# touching web/locales/en.json or a language-pack locale file, or
# periodically as a sweep.
#
# Scope: nav.* and designer.title/journal.title (the two screen titles
# that mirror a nav entry today) across every locale this checkout can
# see — the core repo's own web/locales/*.json, plus any
# ut-plugin-language-{de,es} pack cloned as a SIBLING directory next to
# this repo (best-effort: most checkouts won't have those, so their
# absence is reported, not treated as a failure).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

BASE="web/locales/en.json"
KEY_PATTERN='^(nav\.|designer\.title$|journal\.title$)'

# Known, reviewed loanwords: identical-to-English is the correct value,
# not a bug. Add a row here (with a one-line reason) only after an actual
# human decision, same convention as guard-i18n.sh's i18n:ignore.
declare -A ALLOWLIST=(
  ["nav.github"]="GitHub is a brand name, never translated"
  ["nav.plugins"]="\"Plugins\" is an established tech loanword in de/es POS UIs"
)

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required by this script" >&2
  exit 1
fi

audit_locale() {
  local label="$1" file="$2"
  if [[ ! -f "$file" ]]; then
    echo "  (skip) $label: $file not found"
    return
  fi
  local hits=0
  while IFS=$'\t' read -r key en_val loc_val; do
    [[ "$key" =~ $KEY_PATTERN ]] || continue
    [[ "$en_val" == "$loc_val" ]] || continue
    if [[ -n "${ALLOWLIST[$key]:-}" ]]; then
      echo "  OK (loanword)  $key = \"$loc_val\"  — ${ALLOWLIST[$key]}"
    else
      echo "  ⚠ IDENTICAL    $key = \"$loc_val\"  (same as en) — needs a human look"
      hits=$((hits + 1))
    fi
  done < <(jq -r --slurpfile en "$BASE" '
      . as $loc
      | ($en[0] | keys_unsorted[]) as $k
      | select($loc[$k] != null)
      | [$k, $en[0][$k], $loc[$k]] | @tsv
    ' "$file")
  if [[ "$hits" -eq 0 ]]; then
    echo "  $label: clean (no unreviewed identical menu-level strings)"
  fi
}

echo "== nav.*/screen-title parity audit against $BASE =="
echo "-- core repo locales --"
for f in web/locales/*.json; do
  [[ "$(basename "$f")" == "en.json" ]] && continue
  audit_locale "$(basename "$f" .json)" "$f"
done

echo "-- sibling language packs (../ut-plugin-language-{de,es}) --"
audit_locale "de (pack)" "../ut-plugin-language-de/locales/de.json"
audit_locale "es (pack)" "../ut-plugin-language-es/locales/es.json"

echo "== done — review any ⚠ line above; add a reviewed ALLOWLIST entry only for a real loanword =="
