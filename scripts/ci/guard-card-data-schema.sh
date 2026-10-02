#!/usr/bin/env bash
#
# Card-data schema guard (ADR-0127 §7c, ut-docs#3372).
#
# ADR-0127: card data lives only in the PSP's vault. We may keep last4,
# brand, exp_month/exp_year and PSP token/customer ids (§4) -- never a card
# number, BIN/IIN, CVV/CVC, track data or a PIN block. This guard fails when
# a migration names a column (or anything else) like one of those, so the
# schema can't quietly grow a place to put them before any saved-card
# feature ships.
#
# How it matches: every identifier left after stripping `--` comments,
# `/* */` comments (also across lines) and '...' string literals is turned
# camelCase -> snake_case, lower-cased and split on `_` (a "quoted name"
# with spaces or dashes is split on those too). It is a hit when any segment
# is a forbidden word (pan, bin, iin, cvv, cvc, csc, magstripe, ...) or one
# with digits glued on (pan2, track3, first6digits), or the identifier
# contains a forbidden phrase (card_number, pin_block, track_data, ...).
# `binary`, `pane`, `tracking`, `basin` don't match: segments are whole
# words. `bin` will also hit a stock-bin column (`storage_bin_id`): use the
# marker with that reason.
#
# A justified exception, two ways (the reason is mandatory):
#   * a shipped migration is frozen (ADR-0100) -- add a line to
#     scripts/ci/card-data-schema-allowlist.txt:
#         <path> <identifier>  # <reason>
#   * a new migration -- same-line marker:  -- card-data:allow <reason>
#     (it exempts the whole line, so keep one column per line).
#
# It is a name check, not a data check: a column called `notes` that a
# handler fills with a card number passes. pos.validateMaskedPAN and the
# ADR-0127 §7b log redaction cover values.
set -euo pipefail

ROOT_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
cd "${ROOT_DIR}"

# Overridable so the self-test runs against scratch fixtures, never the
# real (embedded, append-only) migrations tree.
MIGRATIONS_DIR="${MIGRATIONS_DIR:-internal/db/migrations}"
ALLOWLIST="${CARD_DATA_ALLOWLIST:-scripts/ci/card-data-schema-allowlist.txt}"

if [[ ! -d "${MIGRATIONS_DIR}" ]]; then
  echo "❌ card-data schema guard: ${MIGRATIONS_DIR} does not exist (renamed or missing?)" >&2
  exit 1
fi
shopt -s nullglob
files=("${MIGRATIONS_DIR}"/*.sql)
if [[ ${#files[@]} -eq 0 ]]; then
  echo "❌ card-data schema guard: no *.sql under ${MIGRATIONS_DIR} -- refusing to pass vacuously" >&2
  exit 1
fi
allow_file=/dev/null
if [[ -f "${ALLOWLIST}" ]]; then allow_file="${ALLOWLIST}"; fi

set +e
out="$(awk -v allow_path="${allow_file}" '
BEGIN {
  # Whole-segment words (identifier split on "_").
  n = split("pan bin iin cvv cvv2 cvc cvc2 cav2 csc cvn magstripe pinblock track1 track2 track3 trackdata first6 firstsix cardnumber cardnum cardno ccnum ccnumber", w, " ")
  for (i = 1; i <= n; i++) seg[w[i]] = 1
  # Multi-segment phrases, matched inside "_<identifier>_".
  n = split("card_number card_num card_no cc_number cc_num pin_block track_1 track_2 track_data first_6 first_six primary_account_number card_verification card_security_code", w, " ")
  for (i = 1; i <= n; i++) phrase[w[i]] = 1
  bad_allow = 0
  while ((getline line < allow_path) > 0) {
    if (line ~ /^[ \t]*(#|$)/) continue
    if (line !~ /#[ \t]*[^ \t]/) { print "allow-list line has no reason: " line; bad_allow = 1; continue }
    split(line, a, /[ \t]+/)
    allowed[a[1] SUBSEP tolower(a[2])] = 1
  }
}
# camelCase / PascalCase -> snake_case, lower-cased.
function snake(s,    out, i, c, prev) {
  out = ""; prev = ""
  for (i = 1; i <= length(s); i++) {
    c = substr(s, i, 1)
    if (c ~ /[A-Z]/ && prev ~ /[a-z0-9]/) out = out "_"
    out = out c; prev = c
  }
  return tolower(out)
}
function hit(id,    s, k, parts, n) {
  n = split(id, parts, "_")
  for (k = 1; k <= n; k++) {
    if (parts[k] in seg) return parts[k]
    # Digits glued on: pan2, track3, first6digits.
    if (parts[k] ~ /^(pan|bin|iin|cvv|cvc|track)[0-9]+$/ || parts[k] ~ /^first(6|six)/) return parts[k]
  }
  s = "_" id "_"
  for (k in phrase) if (index(s, "_" k "_")) return k
  return ""
}
FNR == 1 { in_block = 0 }
{
  line = $0
  # A /* ... */ comment, possibly spanning lines.
  if (in_block) {
    if (!match(line, /\*\//)) next
    line = substr(line, RSTART + RLENGTH); in_block = 0
  }
  while (match(line, /\/\*/)) {
    head = substr(line, 1, RSTART - 1); tail = substr(line, RSTART + 2)
    if (match(tail, /\*\//)) { line = head " " substr(tail, RSTART + RLENGTH) }
    else { line = head; in_block = 1 }
  }
  gsub(/'\''[^'\'']*'\''/, "", line)
  # The marker counts only in a real -- comment (strings are gone now).
  if (line ~ /--.*card-data:allow[ \t]+[^ \t]/) next
  sub(/--.*$/, "", line)
  # "quoted name" with spaces/dashes -> one underscore-joined identifier.
  out = ""
  while (match(line, /"[^"]*"/)) {
    q = substr(line, RSTART + 1, RLENGTH - 2); gsub(/[ -]+/, "_", q)
    out = out substr(line, 1, RSTART - 1) " " q " "; line = substr(line, RSTART + RLENGTH)
  }
  line = out line
  while (match(line, /[A-Za-z_][A-Za-z0-9_]*/)) {
    tok = substr(line, RSTART, RLENGTH)
    line = substr(line, RSTART + RLENGTH)
    id = snake(tok)
    why = hit(id)
    if (why == "") continue
    if ((FILENAME SUBSEP id) in allowed) continue
    printf "%s:%d: identifier \"%s\" looks like card data (%s)\n", FILENAME, FNR, tok, why
    found = 1
  }
}
END { exit (found || bad_allow) ? 1 : 0 }
' "${files[@]}")"
rc=$?
set -e

if [[ ${rc} -ne 0 ]]; then
  echo "${out}" >&2
  cat >&2 <<'MSG'
❌ card-data schema guard (ADR-0127 §7c): the schema must not name a card
   number, BIN/IIN, CVV/CVC, track data or a PIN block. Allowed by design:
   last4, brand, exp_month, exp_year, PSP token/customer ids (ADR-0127 §4).
   A genuine false positive: same-line `-- card-data:allow <reason>`, or for
   a shipped (frozen) migration an entry in
   scripts/ci/card-data-schema-allowlist.txt with a reason.
MSG
  exit 1
fi
echo "✅ card-data schema guard: ${#files[@]} migration(s) clean"
