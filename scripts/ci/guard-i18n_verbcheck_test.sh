#!/usr/bin/env bash
#
# Regression test for guard-i18n.sh's check 8 (ut-docs#1865): proves the
# guard actually flags a dropped/invented/reordered-without-declaring-it
# format or template verb between en.json and a locale file, proves a
# plain-prose "%" (e.g. "10%-off") is never mistaken for a verb, proves
# Go's explicit positional verbs (%[1]s) may legitimately reorder, proves
# mixing positional and implicit verbs in one string is rejected rather
# than guessed at, and proves the guard still passes on the real,
# unmodified locale files.
#
# Different mechanism from guard-i18n_test.sh/_toast_test.sh/_keycall_test.sh:
# those plant disposable NEW fixture files. Check 8 operates on the
# existing web/locales/*.json files themselves, so this test instead adds
# one throwaway key to en.json and to one target locale, runs the guard,
# then removes exactly that key again — restoring both files byte-for-byte
# via the original content captured up front, never a partial JSON rewrite.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-i18n.sh"
EN="web/locales/en.json"
LOC="web/locales/tr.json"
OTHER_LOCALES=("web/locales/ar.json" "web/locales/fa.json")
FAIL_COUNT=0

BACKUP_DIR="$(mktemp -d)"
cp "$EN" "${BACKUP_DIR}/en.json"
cp "$LOC" "${BACKUP_DIR}/loc.json"
for f in "${OTHER_LOCALES[@]}"; do
  cp "$f" "${BACKUP_DIR}/$(basename "$f").bak"
done

# Byte-for-byte restore from the backed-up files -- NOT a `$(cat ...)`
# variable capture, which strips trailing newlines and would leave every
# case's cleanup one byte off from the real original.
restore() {
  cp "${BACKUP_DIR}/en.json" "$EN"
  cp "${BACKUP_DIR}/loc.json" "$LOC"
  for f in "${OTHER_LOCALES[@]}"; do
    cp "${BACKUP_DIR}/$(basename "$f").bak" "$f"
  done
}
cleanup() {
  local status=$?
  restore
  rm -rf "${BACKUP_DIR}"
  exit "${status}"
}
trap cleanup EXIT

# plant KEY EN_VALUE LOC_VALUE
# Adds one throwaway key to en.json, the target locale (LOC), and every
# OTHER real locale file (set to the same value as en.json, so check 2's
# unrelated key-parity comparison never fires and only check 8's verb
# comparison against LOC is actually being exercised) -- python, so JSON
# stays valid regardless of quoting in the values. Starts fresh from the
# backed-up originals each time so cases never stack on top of each other.
plant() {
  local key="$1" en_val="$2" loc_val="$3"
  restore
  python3 - "$EN" "$key" "$en_val" <<'PY'
import json, sys
path, key, val = sys.argv[1:4]
d = json.load(open(path))
d[key] = val
json.dump(d, open(path, "w"), ensure_ascii=False, indent=2, sort_keys=True)
PY
  python3 - "$LOC" "$key" "$loc_val" <<'PY'
import json, sys
path, key, val = sys.argv[1:4]
d = json.load(open(path))
d[key] = val
json.dump(d, open(path, "w"), ensure_ascii=False, indent=2, sort_keys=True)
PY
  for f in "${OTHER_LOCALES[@]}"; do
    python3 - "$f" "$key" "$en_val" <<'PY'
import json, sys
path, key, val = sys.argv[1:4]
d = json.load(open(path))
d[key] = val
json.dump(d, open(path, "w"), ensure_ascii=False, indent=2, sort_keys=True)
PY
  done
}

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_verbcheck_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_i18n_verbcheck_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -q "format/template verb mismatch" /tmp/guard_i18n_verbcheck_test_out.$$; then
    echo "❌ FAIL: guard rejected ${label} for the WRONG reason:" >&2
    cat /tmp/guard_i18n_verbcheck_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
  rm -f /tmp/guard_i18n_verbcheck_test_out.$$
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_verbcheck_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_i18n_verbcheck_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_i18n_verbcheck_test_out.$$
}

# A dropped %d must be rejected.
plant "zz.verb.dropped" "Remove %d items" "Remove items"
expect_fail "a dropped %d verb"

# An invented verb (not present in en.json) must be rejected.
plant "zz.verb.invented" "Remove items" "Remove %d items"
expect_fail "an invented %d verb"

# %% is a literal percent sign, never a verb -- must never trip the guard,
# and must not swallow a real trailing verb into itself either.
plant "zz.verb.percent-literal" "100%% done, %d left" "100%% fertig, %d verbleibend"
expect_pass "a %% literal alongside a real verb"

# A dropped %% (ut-docs#1873): the locale value loses one of the two `%`
# characters in the literal-percent pair. This extracts the IDENTICAL
# verb-token list on both sides (['%d'] -- %% is discarded as "not a
# verb" either way), so the verb-token comparison alone passes vacuously;
# only a separate count-of-%%-occurrences check catches it. Left
# unfixed, this is the exact %!d(MISSING)-class corruption check 8 exists
# to prevent.
plant "zz.verb.percent-dropped" "50%% off, %d left" "50% off, %d left"
expect_fail "a dropped %% (locale lost one of the two literal percent characters)"

# Plain English "%" immediately followed by a flag-shaped letter (the
# real false positive found against this repo's own en.json while writing
# this check: "a 10%-off code") must never be mistaken for a verb.
plant "zz.verb.percent-prose" "includes a 10%-off code" "enthaelt einen 10%-off Code"
expect_pass "percent sign in prose, not a verb"

# A PLAIN (non-positional) reorder is a real bug -- writing order IS
# argument order for %s/%d, so this must still fail.
plant "zz.verb.plain-reorder" "%d of %s" "%s von %d"
expect_fail "a plain reorder without positional verbs"

# Go's EXPLICIT positional verbs may legitimately reorder -- the index,
# not the writing order, says which argument goes where.
plant "zz.verb.positional-reorder" "%[1]d of %[2]s" "%[2]s: %[1]d"
expect_pass "an explicit positional-verb reorder"

# A positional verb whose SHAPE changes at a given index must still fail
# -- reordering is the only thing the positional exception permits, not
# a free pass on verb identity. Independent review found the previous
# reorder case alone doesn't discriminate: with no positional support at
# all, "%[1]d of %[2]s" extracts zero tokens on both sides and passes
# vacuously. This case only passes with real positional-verb comparison.
plant "zz.verb.positional-shape-changed" "%[1]d of %[2]s" "%[1]s of %[2]s"
expect_fail "a positional verb whose shape changed at the same index"

# Mixing positional and implicit verbs in the same string can't be safely
# verified, and must be rejected rather than guessed at.
plant "zz.verb.mixed" "%[1]d of %s" "%[1]d von %s"
expect_fail "a string mixing positional and implicit verbs"

# Template tokens ({{name}}, {0}) must still be checked exactly as before
# (unaffected by the printf-verb hardening in this same check).
plant "zz.verb.template-dropped" "Hi {{name}}, see {0}" "Hallo, siehe {0}"
expect_fail "a dropped {{name}} template token"

# A positional printf verb alongside a template token is a DIFFERENT
# dialect pairing, not a mixed-printf-dialect string -- must not be
# rejected as "mixed positional/implicit" just because a template token
# has no positional index of its own (ut-docs#1865 review finding 1: the
# original implementation made this combination unsatisfiable by ANY
# translation, including a byte-for-byte copy of en.json).
plant "zz.verb.positional-with-template" "%[1]d of {{name}}" "{{name}}: %[1]d"
expect_pass "a positional verb alongside a template token"

# The un-hyphenated sibling of the original false positive (ut-docs#1865
# review finding 4): "%" immediately followed by digits then a verb
# letter THEN more letters (no word boundary) must not be mistaken for a
# verb with trailing prose glued on.
plant "zz.verb.percent-prose-no-hyphen" "including a 10%off code" "enthaelt einen 10%off Code"
expect_pass "percent sign directly followed by prose letters, not a verb"

restore
expect_pass "the real, unmodified repository tree"

echo
if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ guard-i18n_verbcheck_test.sh: ${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ guard-i18n_verbcheck_test.sh: all cases passed"
