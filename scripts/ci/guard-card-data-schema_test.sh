#!/usr/bin/env bash
#
# Regression test for guard-card-data-schema.sh (ADR-0127 §7c, ut-docs#3372).
# The failure that matters is the guard silently ACCEPTING a card-number
# column after a regex tweak, which looks exactly like "clean" in CI -- so
# the reject cases are asserted as hard as the accepts, and the near-misses
# (binary, tracking, last4) get their own cases.
set -euo pipefail

GUARD="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/guard-card-data-schema.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

failures=0

# case_ <name> <expect: pass|fail> <migration body> [allow-list body]
case_() {
  local name="$1" expect="$2" body="$3" allow="${4:-}"
  local dir="${TMP}/${name}"
  mkdir -p "${dir}/m"
  printf '%s\n' "${body}" > "${dir}/m/001_x.sql"
  printf '%s\n' "${allow}" > "${dir}/allow.txt"
  local out rc=0
  out="$(MIGRATIONS_DIR="${dir}/m" CARD_DATA_ALLOWLIST="${dir}/allow.txt" bash "${GUARD}" 2>&1)" || rc=$?
  if [[ "${expect}" == pass && ${rc} -ne 0 ]]; then
    echo "FAIL: ${name} -- expected pass, guard exited ${rc}"
    # shellcheck disable=SC2001  # multi-line indent; ${x//} can't do per-line
    echo "${out}" | sed 's/^/    /'
    failures=$((failures + 1))
  elif [[ "${expect}" == fail && ${rc} -eq 0 ]]; then
    echo "FAIL: ${name} -- expected the guard to REJECT this, it passed"
    failures=$((failures + 1))
  else
    echo "ok: ${name} (${expect})"
  fi
}

# --- rejects: every ADR-0127 §7c family ---
case_ pan            fail 'ALTER TABLE payments ADD COLUMN pan TEXT;'
case_ card-number    fail 'CREATE TABLE saved_cards (id TEXT, card_number TEXT);'
case_ cardnumber     fail 'CREATE TABLE c (CardNumber TEXT);'
case_ bin            fail 'ALTER TABLE payments ADD COLUMN card_bin TEXT;'
case_ iin            fail 'ALTER TABLE payments ADD COLUMN iin TEXT;'
case_ cvv            fail 'ALTER TABLE saved_cards ADD COLUMN cvv TEXT;'
case_ cvc2           fail 'ALTER TABLE saved_cards ADD COLUMN cvc2 TEXT;'
case_ track2         fail 'ALTER TABLE p ADD COLUMN track2 TEXT;'
case_ track-1        fail 'ALTER TABLE p ADD COLUMN track_1_data TEXT;'
case_ track-data     fail 'ALTER TABLE p ADD COLUMN raw_track_data BLOB;'
case_ pin-block      fail 'ALTER TABLE p ADD COLUMN pin_block TEXT;'
case_ first6         fail 'ALTER TABLE p ADD COLUMN first6 TEXT;'
case_ first-six      fail 'ALTER TABLE p ADD COLUMN card_first_six TEXT;'
case_ quoted-ident   fail 'CREATE TABLE p ("pan" TEXT);'
case_ index-name     fail 'CREATE INDEX idx_cards_pan ON cards(id);'
# Review findings (ut-docs#3372): camelCase, glued digits, more names.
case_ camel-pan      fail 'ALTER TABLE p ADD COLUMN fullPan TEXT;'
case_ camel-cvv      fail 'ALTER TABLE p ADD COLUMN cardCvv TEXT;'
case_ glued-pan2     fail 'ALTER TABLE p ADD COLUMN pan2 TEXT;'
case_ glued-track3   fail 'ALTER TABLE p ADD COLUMN track3 TEXT;'
case_ glued-first6   fail 'ALTER TABLE p ADD COLUMN first6digits TEXT;'
case_ magstripe      fail 'ALTER TABLE p ADD COLUMN magstripe_data BLOB;'
case_ csc            fail 'ALTER TABLE p ADD COLUMN csc TEXT;'
case_ quoted-spaces  fail 'CREATE TABLE p ("card number" TEXT);'
case_ rename-column  fail 'ALTER TABLE p RENAME COLUMN ref TO pan;'
case_ multiline-table fail 'CREATE TABLE saved_cards (
    id TEXT PRIMARY KEY,
    cvv TEXT
);'
# A one-line /* */ comment on each side must not swallow the column between.
case_ block-comments-both-sides fail 'CREATE TABLE p (a TEXT, /* a */ cvv TEXT /* b */);'
# A marker inside a string that itself contains "--" is still not a comment.
case_ marker-in-dash-string fail "ALTER TABLE p ADD COLUMN cvv TEXT DEFAULT '-- card-data:allow x';"
# A comment after the column doesn't hide the column.
case_ trailing-comment fail 'ALTER TABLE p ADD COLUMN cvv TEXT; -- temporary'
# A bare marker (no reason) exempts nothing.
case_ bare-marker    fail 'ALTER TABLE p ADD COLUMN cvv TEXT; -- card-data:allow'
# The marker must be in a comment, not smuggled into a string literal.
case_ marker-in-string fail "ALTER TABLE p ADD COLUMN cvv TEXT DEFAULT 'card-data:allow x';"
# An allow-list entry without a reason is itself a failure.
case_ allow-no-reason fail 'ALTER TABLE p ADD COLUMN masked_pan TEXT;' "${TMP}/allow-no-reason/m/001_x.sql masked_pan"
# An entry for another file doesn't exempt this one.
case_ allow-wrong-file fail 'ALTER TABLE p ADD COLUMN masked_pan TEXT;' 'other/001_x.sql masked_pan  # reason'

# --- accepts: ADR-0127 §4 fields and whole-word near-misses ---
case_ allowed-fields pass 'CREATE TABLE saved_cards (id TEXT, last4 TEXT, brand TEXT, exp_month INTEGER, exp_year INTEGER, psp_token TEXT, psp_customer_id TEXT);'
case_ near-misses    pass 'CREATE TABLE t (data BINARY, tracking_ref TEXT, pane_id TEXT, basin TEXT, spanish TEXT, company TEXT, binding TEXT, pin_hash TEXT);'
case_ in-comment     pass '-- never store a card_number or cvv here (ADR-0127)
CREATE TABLE t (id TEXT);'
case_ in-string      pass "INSERT INTO settings (key, value) VALUES ('help', 'enter the card number on the terminal');"
case_ block-comment-multiline pass '/*
   never store the pan or cvv here (ADR-0127)
*/
CREATE TABLE t (id TEXT);'
case_ bin-with-marker pass 'ALTER TABLE stock ADD COLUMN storage_bin_id TEXT; -- card-data:allow warehouse bin, not a card BIN'
case_ marker         pass 'ALTER TABLE p ADD COLUMN pan_zoom INTEGER; -- card-data:allow image pan, not a card'
case_ allow-listed   pass 'ALTER TABLE p ADD COLUMN masked_pan TEXT;' "${TMP}/allow-listed/m/001_x.sql masked_pan  # scheme+last4 only"

# The real tree, with the real allow-list, must be clean.
if ! bash "${GUARD}" >/dev/null 2>&1; then
  echo "FAIL: real migrations tree -- guard rejects the shipped schema"
  failures=$((failures + 1))
else
  echo "ok: real migrations tree (pass)"
fi

if [[ ${failures} -ne 0 ]]; then
  echo "guard-card-data-schema_test: ${failures} case(s) failed" >&2
  exit 1
fi
echo "guard-card-data-schema_test: all cases passed"
