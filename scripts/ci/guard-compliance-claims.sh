#!/usr/bin/env bash
#
# Guard: the compliance wording the product owner approved on ut-docs#667
# (2026-08-13) is enforced by CI, not left to memory. ADR-0040 already binds
# this in principle — "No GoBD/§147 AO or other compliance certification is
# claimed anywhere in code, UI copy, or commit messages" — but an approved
# list that only lives in an issue comment is violated by the first person
# writing German marketing copy who never read it. Per the ecosystem's
# standing rule that every mistake gets a check rather than only a fix, this
# is that check.
#
# We may describe what the software DOES (a factual capability). We may NOT
# promise a legal OUTCOME, because the outcome depends on how the shop
# actually operates — that is the line the approved denylist below draws.
#
# Scans three surfaces, in whatever real content ships to a shop owner or an
# operator's screen:
#   - web/locales/*.json  — every locale's translated UI strings
#   - web/help/**         — the user manual
#   - web/ui/**           — template copy (catches a hardcoded literal that
#                           bypassed the i18n keys entirely, same blind spot
#                           guard-i18n.sh's own check 5 documents)
#
# Case- and language-insensitive: the German forms (finanzamtskonform,
# revisionssicher, …) are where the actual pilot risk sits, not the English
# ones — that's a deliberate emphasis, not an oversight.
#
# Escape hatch: a same-line `compliance-claim:allow` marker (same convention
# as guard-i18n.sh's `i18n:ignore`) — for a reviewed exception, e.g. a help
# topic explaining IN PROSE why we do not use a forbidden term (quoting it to
# explain it is not a violation of the rule the quote itself states). Applies
# to web/help/** and web/ui/** only — web/locales/*.json is plain JSON with
# no comment syntax to carry a marker, so a false positive there means
# rewriting the string, not suppressing the check. In practice this is not a
# real gap: translated UI copy has no legitimate reason to quote a forbidden
# compliance phrase the way an explanatory doc might.
#
# Known detection gaps, accepted (review, ut-docs#681) — this is a
# line-based literal-substring check, so it cannot see a forbidden term that
# is: split across a line wrap (this repo's help topics are hard-wrapped
# prose, so a two-word term like "fully compliant" straddling a wrap is a
# real, not theoretical, miss), split by an HTML tag or template action,
# separated by doubled/non-breaking whitespace or a JSON \n escape, or
# spelled with a Unicode look-alike hyphen/dash instead of ASCII "-". None of
# these are worth the complexity of fixing (this checks a denylist, it is
# not a copy reviewer — see the card's own non-goals) — noted so the next
# reader doesn't over-trust this as an exhaustive check.
#
# Explicit-args form for fixture-based testing (see
# guard-compliance-claims_test.sh): args are locales dir, help dir, ui dir.
set -euo pipefail

# ut-docs#662: this repo's own CI runner has no LANG set, i.e. the C locale —
# and `grep -i` only case-folds non-ASCII letters (e.g. Ü→ü) in a UTF-8
# locale. Without this, the one German term containing a cased umlaut
# ("wir übernehmen ihre §146a-anmeldung") is missed in its capitalized forms
# on the exact runner this guard exists to protect — the same locale-
# dependence bug class ut-docs#662 already found once, now in a guard whose
# whole point is catching the German forms. Force UTF-8 regardless of the
# runner's own environment, rather than relying on it being set correctly.
export LC_ALL=C.UTF-8

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LOCALES_DIR="${ROOT_DIR}/web/locales"
HELP_DIR="${ROOT_DIR}/web/help"
UI_DIR="${ROOT_DIR}/web/ui"

if [ "$#" -ge 1 ]; then LOCALES_DIR="$1"; fi
if [ "$#" -ge 2 ]; then HELP_DIR="$2"; fi
if [ "$#" -ge 3 ]; then UI_DIR="$3"; fi

for d in "$LOCALES_DIR" "$HELP_DIR" "$UI_DIR"; do
  if [ ! -d "$d" ]; then
    echo "❌ compliance-claims guard: ${d} does not exist" >&2
    exit 1
  fi
done

# ut-docs#667's approved forbidden list, verbatim (English + German outcome
# claims), plus concrete phrasings of "we filed/will file your §146a
# notification for you" — that last bullet is deliberately open-ended in the
# approval ("anything implying we have filed or will file …"), so this list
# covers the phrasings actually seen so far, not every possible wording; add
# to it as new ones turn up, same living-list spirit as the other guards in
# this file (this checks a denylist, it is not a copy reviewer — see the
# card's own non-goals).
FORBIDDEN_TERMS=(
  "gobd-compliant"
  "gobd-konform"
  "kassensichv-compliant"
  "kassensichv-konform"
  "finanzamtskonform"
  "audit-proof"
  "revisionssicher"
  "certified by the finanzamt"
  "vom finanzamt zertifiziert"
  "approved by the tax office"
  "you are compliant"
  "fully compliant"
  "we file your §146a"
  "we submit your §146a"
  "we will file your §146a"
  "we handle your §146a filing"
  "we take care of your §146a"
  "wir melden ihre kasse"
  "wir reichen ihre §146a"
  "wir übernehmen ihre §146a-anmeldung"
)

ALLOW_MARKER="compliance-claim:allow"

failed=0
checked=0

# One term-per-line pattern file for a single `grep -n -i -F -f` pass per
# scanned file, instead of a bash while-read loop calling tr/grep per LINE —
# the naive version of this guard did that and took minutes to scan the real
# tree (thousands of locale-JSON lines x 19 terms, each a forked process).
TERMS_FILE="$(mktemp)"
trap 'rm -f "${TERMS_FILE}"' EXIT
printf '%s\n' "${FORBIDDEN_TERMS[@]}" >"${TERMS_FILE}"

# scan_file PATH ALLOW_HATCH — ALLOW_HATCH=1 lets a same-line
# compliance-claim:allow marker suppress a match (help/UI); 0 does not
# (locale JSON, which has no comment syntax to carry the marker).
scan_file() {
  local file="$1" allow_hatch="$2"
  local rel="${file#"${ROOT_DIR}/"}"
  local matches lineno content
  matches="$(grep -n -i -F -f "$TERMS_FILE" "$file" || true)"
  [ -z "$matches" ] && return
  while IFS= read -r m; do
    lineno="${m%%:*}"
    content="${m#*:}"
    if [ "$allow_hatch" = "1" ] && printf '%s' "$content" | grep -qF -- "$ALLOW_MARKER"; then
      continue
    fi
    echo "❌ compliance-claims guard: ${rel}:${lineno} contains a forbidden compliance claim:" >&2
    echo "   ${content}" >&2
    echo "   (ut-docs#667 approved wording list — this asserts a legal outcome" >&2
    echo "   we are not entitled to claim). Rephrase as a factual capability," >&2
    echo "   or mark a reviewed exception with a same-line ${ALLOW_MARKER}." >&2
    failed=1
  done <<<"$matches"
}

locales_checked=0
while IFS= read -r -d '' f; do
  locales_checked=$((locales_checked + 1))
  scan_file "$f" 0
done < <(find "$LOCALES_DIR" -maxdepth 1 -name '*.json' -print0)

help_checked=0
while IFS= read -r -d '' f; do
  help_checked=$((help_checked + 1))
  scan_file "$f" 1
done < <(find "$HELP_DIR" -name '*.md' -print0)

ui_checked=0
while IFS= read -r -d '' f; do
  ui_checked=$((ui_checked + 1))
  scan_file "$f" 1
done < <(find "$UI_DIR" -name '*.html' -print0)

# Fail closed PER SURFACE, not just when all three come up empty together —
# the realistic drift is one surface moving or being renamed (e.g. help
# topics going .mdx, templates going .gohtml), not the whole tree vanishing
# at once. A single combined counter would stay green while one whole
# surface silently stopped being scanned (review finding, ut-docs#681).
if [ "$locales_checked" -eq 0 ]; then
  echo "❌ compliance-claims guard: no *.json files found under ${LOCALES_DIR#"${ROOT_DIR}/"} — locale strings are no longer being scanned." >&2
  exit 1
fi
if [ "$help_checked" -eq 0 ]; then
  echo "❌ compliance-claims guard: no *.md files found under ${HELP_DIR#"${ROOT_DIR}/"} — the manual is no longer being scanned." >&2
  exit 1
fi
if [ "$ui_checked" -eq 0 ]; then
  echo "❌ compliance-claims guard: no *.html files found under ${UI_DIR#"${ROOT_DIR}/"} — UI templates are no longer being scanned." >&2
  exit 1
fi
checked=$((locales_checked + help_checked + ui_checked))

if [ "$failed" -ne 0 ]; then
  exit 1
fi

echo "✓ compliance-claims guard: ${checked} file(s) scanned, no forbidden fiscal-compliance claims found"
