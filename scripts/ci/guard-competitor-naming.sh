#!/usr/bin/env bash
#
# Guard: ADR-0106 Decision D's binding naming rule (ut-docs#1905), enforced
# by CI rather than left to memory — the same shape as
# guard-compliance-claims.sh, which is the explicit template that ADR names.
#
# No layout preset — and by the same logic no `layout`, `theme` or future
# `vertical` plugin — is named after a competitor, or described in its
# manifest, code, docs or marketing as imitating one. Presets are named for
# what they do (Familiar, Classic, Compact), never for who they resemble.
#
# Two tiers, because a competitor's NAME is not by itself a violation —
# this product legitimately recognises a SumUp/Square catalog export and
# integrates SumUp as a payment provider, and the help says so:
#
#   1. IMITATION PHRASINGS ("SumUp-style", "like Zettle", "wie ready2order",
#      "inspired by Lightspeed", "Shopify look", …) are forbidden on every
#      surface that reaches a shop owner or a plugin author:
#        - web/locales/*.json     — every locale's translated UI strings
#        - web/help/**/*.md       — the user manual
#        - web/ui/**/*.html       — template copy
#        - plugins/**/plugin.json — first-party manifests (name, description,
#                                   labels), plus each plugin's own
#                                   locales/*.json, which is where a
#                                   key-shaped `label` actually resolves
#   2. BARE NAMES are additionally forbidden in a PRESENTATION plugin's
#      manifest and its locales — a `plugin.json` whose canonical_type is
#      `layout` or `theme` (the ADR-0106 D types; `vertical` is not a
#      canonical type yet and would join this list when it is). That is the
#      ADR's actual point: "SumUp" is never a preset's name, however
#      phrased. A payment plugin under plugins/ may still name the provider
#      it integrates.
#
# Denylist: the names in ut-docs/reference/competitor-research/ (SumUp) and
# the standing research list in the scrum-master skill's STANDING-CONTEXT
# (Square, Zettle, ready2order, Lightspeed, Toast, Shopify). Square and
# Toast are ordinary English words (square corners, a toast notification —
# both are real in this UI), so bare they would false-positive everywhere;
# only their product forms are listed (Square POS, SquareUp, Toast POS,
# ToastTab) plus the hyphenated imitation forms that cannot mean anything
# else ("square-style"). Living list: add a name as research adds one.
#
# Case-insensitive (`grep -i` under a forced UTF-8 locale — see below).
#
# Escape hatch: a same-line `naming-rule:allow` marker (same convention as
# `compliance-claim:allow` and `i18n:ignore`) — for a reviewed exception,
# e.g. a help topic or a template comment that QUOTES a competitor to explain
# the rule or record a product-owner comparison, not to imitate them.
# Applies to web/help/** and web/ui/** only — locale JSON and plugin.json
# are plain JSON with no comment syntax to carry a marker, so a match there
# means rewriting the string, not suppressing the check. That is not a real
# gap for the surfaces this rule exists for: a preset's name has no
# legitimate reason to contain a competitor's.
#
# Known detection gaps, accepted (same class as the compliance guard's): a
# line-based literal-substring check cannot see a phrase split across a
# hard-wrapped line, an HTML tag or a JSON \n escape; and "unlike SumUp"
# contains "like sumup" (a contrast, not an imitation — rephrase or mark it).
# This checks a denylist; it is not a copy reviewer.
#
# Explicit-args form for fixture-based testing (see
# guard-competitor-naming_test.sh): args are locales dir, help dir, ui dir,
# plugins dir.
set -euo pipefail

# Same reason as guard-compliance-claims.sh (ut-docs#662): the CI runner has
# no LANG set, and `grep -i` only case-folds non-ASCII letters in a UTF-8
# locale. Nothing on today's list carries one, but the German help is a
# scanned surface and the list is living — force UTF-8 rather than depend on
# the runner.
export LC_ALL=C.UTF-8

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCALES_DIR="${ROOT_DIR}/web/locales"
HELP_DIR="${ROOT_DIR}/web/help"
UI_DIR="${ROOT_DIR}/web/ui"
PLUGINS_DIR="${ROOT_DIR}/plugins"

if [ "$#" -ge 1 ]; then LOCALES_DIR="$1"; fi
if [ "$#" -ge 2 ]; then HELP_DIR="$2"; fi
if [ "$#" -ge 3 ]; then UI_DIR="$3"; fi
if [ "$#" -ge 4 ]; then PLUGINS_DIR="$4"; fi

for d in "$LOCALES_DIR" "$HELP_DIR" "$UI_DIR" "$PLUGINS_DIR"; do
  if [ ! -d "$d" ]; then
    echo "❌ competitor-naming guard: ${d} does not exist" >&2
    exit 1
  fi
done

# Names that are unambiguous as a bare word.
COMPETITOR_NAMES=(
  "sumup"
  "zettle"
  "izettle"
  "ready2order"
  "lightspeed"
  "shopify"
)
# Product forms: the two ordinary-word names (Square, Toast) are ONLY
# listed this way, and "Shopify POS" is listed as the ADR spells it so its
# imitation forms ("Shopify POS layout") are generated alongside the bare
# name's.
COMPETITOR_PRODUCT_NAMES=(
  "square pos"
  "squareup"
  "square register"
  "toast pos"
  "toasttab"
  "toast tab"
  "shopify pos"
)
# "<name><suffix>" and "<prefix><name>" — the shapes "described as imitating"
# actually takes in English and German copy.
IMITATION_SUFFIXES=("-style" " style" "-like" "-inspired" " look" " layout" " preset" " mode" " theme" " clone")
IMITATION_PREFIXES=("like " "wie " "inspired by " "similar to " "familiar from " "modelled on " "modeled on " "clone of ")
# Hyphenated forms of the two ambiguous names that cannot mean anything but
# the competitor.
AMBIGUOUS_IMITATION_TERMS=("square-style" "square-like" "square-inspired" "toast-style" "toast-like" "toast-inspired")

ALLOW_MARKER="naming-rule:allow"

# One term-per-line pattern file per tier for a single `grep -n -i -F -f`
# pass per scanned file (the compliance guard's lesson: a per-line bash loop
# took minutes on the real tree).
IMITATION_TERMS_FILE="$(mktemp)"
ALL_TERMS_FILE="$(mktemp)"
trap 'rm -f "${IMITATION_TERMS_FILE}" "${ALL_TERMS_FILE}"' EXIT
{
  for n in "${COMPETITOR_NAMES[@]}" "${COMPETITOR_PRODUCT_NAMES[@]}"; do
    for s in "${IMITATION_SUFFIXES[@]}"; do printf '%s%s\n' "$n" "$s"; done
    for p in "${IMITATION_PREFIXES[@]}"; do printf '%s%s\n' "$p" "$n"; done
  done
  printf '%s\n' "${AMBIGUOUS_IMITATION_TERMS[@]}"
} >"${IMITATION_TERMS_FILE}"
{
  cat "${IMITATION_TERMS_FILE}"
  printf '%s\n' "${COMPETITOR_NAMES[@]}" "${COMPETITOR_PRODUCT_NAMES[@]}"
} >"${ALL_TERMS_FILE}"

failed=0

# scan_file PATH ALLOW_HATCH TERMS_FILE WHAT — ALLOW_HATCH=1 lets a same-line
# naming-rule:allow marker suppress a match (help/UI); 0 does not (JSON).
# WHAT is the one-line diagnosis printed for a hit.
scan_file() {
  local file="$1" allow_hatch="$2" terms_file="$3" what="$4"
  local rel="${file#"${ROOT_DIR}/"}"
  local matches lineno content
  matches="$(grep -n -i -F -f "$terms_file" "$file" || true)"
  [ -z "$matches" ] && return
  while IFS= read -r m; do
    lineno="${m%%:*}"
    content="${m#*:}"
    if [ "$allow_hatch" = "1" ] && printf '%s' "$content" | grep -qF -- "$ALLOW_MARKER"; then
      continue
    fi
    echo "❌ competitor-naming guard: ${rel}:${lineno} ${what}:" >&2
    echo "   ${content}" >&2
    echo "   (ADR-0106 Decision D — presets, layouts and themes are named for what" >&2
    echo "   they do, never for who they resemble). Rephrase it, or mark a" >&2
    echo "   reviewed exception with a same-line ${ALLOW_MARKER} (help/UI only)." >&2
    failed=1
  done <<<"$matches"
}

# is_presentation_manifest PATH — a plugin.json whose canonical_type is one
# of ADR-0106 D's presentation types, where a bare competitor name is a
# violation on its own.
is_presentation_manifest() {
  grep -q -E '"canonical_type"[[:space:]]*:[[:space:]]*"(layout|theme)"' "$1"
}

locales_checked=0
while IFS= read -r -d '' f; do
  locales_checked=$((locales_checked + 1))
  scan_file "$f" 0 "$IMITATION_TERMS_FILE" "describes the product as imitating a competitor"
done < <(find "$LOCALES_DIR" -maxdepth 1 -name '*.json' -print0)

help_checked=0
while IFS= read -r -d '' f; do
  help_checked=$((help_checked + 1))
  scan_file "$f" 1 "$IMITATION_TERMS_FILE" "describes the product as imitating a competitor"
done < <(find "$HELP_DIR" -name '*.md' -print0)

ui_checked=0
while IFS= read -r -d '' f; do
  ui_checked=$((ui_checked + 1))
  scan_file "$f" 1 "$IMITATION_TERMS_FILE" "describes the product as imitating a competitor"
done < <(find "$UI_DIR" -name '*.html' -print0)

manifests_checked=0
while IFS= read -r -d '' f; do
  manifests_checked=$((manifests_checked + 1))
  terms="$IMITATION_TERMS_FILE"
  what="describes the plugin as imitating a competitor"
  if is_presentation_manifest "$f"; then
    terms="$ALL_TERMS_FILE"
    what="names a competitor in a layout/theme plugin"
  fi
  scan_file "$f" 0 "$terms" "$what"
  # The manifest's `label`s are locale keys — the text a shop owner reads
  # lives in the plugin's own locales/*.json next to it, so it is part of
  # the same surface and scanned under the same tier.
  plugin_dir="$(dirname "$f")"
  if [ -d "${plugin_dir}/locales" ]; then
    while IFS= read -r -d '' l; do
      scan_file "$l" 0 "$terms" "$what"
    done < <(find "${plugin_dir}/locales" -maxdepth 1 -name '*.json' -print0)
  fi
done < <(find "$PLUGINS_DIR" -name 'plugin.json' -print0)

# Fail closed PER SURFACE, not only when everything comes up empty together
# — the realistic drift is one surface moving or being renamed (help topics
# going .mdx, first-party plugins moving out of plugins/), not the whole
# tree vanishing at once (the compliance guard's review finding, ut-docs#681).
if [ "$locales_checked" -eq 0 ]; then
  echo "❌ competitor-naming guard: no *.json files found under ${LOCALES_DIR#"${ROOT_DIR}/"} — locale strings are no longer being scanned." >&2
  exit 1
fi
if [ "$help_checked" -eq 0 ]; then
  echo "❌ competitor-naming guard: no *.md files found under ${HELP_DIR#"${ROOT_DIR}/"} — the manual is no longer being scanned." >&2
  exit 1
fi
if [ "$ui_checked" -eq 0 ]; then
  echo "❌ competitor-naming guard: no *.html files found under ${UI_DIR#"${ROOT_DIR}/"} — UI templates are no longer being scanned." >&2
  exit 1
fi
if [ "$manifests_checked" -eq 0 ]; then
  echo "❌ competitor-naming guard: no plugin.json files found under ${PLUGINS_DIR#"${ROOT_DIR}/"} — plugin manifests are no longer being scanned." >&2
  exit 1
fi
checked=$((locales_checked + help_checked + ui_checked + manifests_checked))

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "✓ competitor-naming guard: ${checked} file(s) scanned, no competitor naming found"
