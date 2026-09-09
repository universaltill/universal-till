#!/usr/bin/env bash
#
# Regression test for guard-i18n.sh's check 9 (ut-docs#1872): proves the
# guard actually flags a locale file that defines the same top-level key
# twice -- the exact shape of bug that shipped when two lanes each added
# the same five keys to a language pack's locale file at a different point
# in the file, and git merged both additions with no conflict, leaving the
# file with five duplicated key definitions that every existing check
# (parsing with plain json.load, which keeps only the last write) reported
# as clean.
#
# Different mechanism from the other guard-i18n_*_test.sh files: this test
# plants a disposable NEW locale file (web/locales/zz_guard_test.json)
# rather than editing en.json or an existing locale, because check 9
# operates on a locale file's own raw text and doesn't need any of the
# other checks' fixtures. The planted file's key set (after normal, last-
# write-wins parsing) is built to exactly match en.json's, so ONLY check 9
# fires -- check 2's unrelated key-parity comparison never fingers this
# fixture as missing/orphan keys, and check 8's verb comparison never
# fingers it either, since every value is copied verbatim from en.json.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-i18n.sh"
FIXTURE="web/locales/zz_guard_test.json"
FAIL_COUNT=0

cleanup() {
  local status=$?
  rm -f "${FIXTURE}"
  exit "${status}"
}
trap cleanup EXIT

expect_fail() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_dupkey_test_out.$$ 2>&1; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    cat /tmp/guard_i18n_dupkey_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard correctly rejected ${label}"
  fi
}

expect_fail_containing() {
  local label="$1"
  shift
  local out rc
  set +e
  out="$(bash "${GUARD}" 2>&1)"
  rc=$?
  set -e
  if [[ ${rc} -eq 0 ]]; then
    echo "❌ FAIL: expected guard to reject ${label}, but it passed" >&2
    echo "${out}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
    return
  fi
  local needle
  for needle in "$@"; do
    if ! grep -qF -- "${needle}" <<<"${out}"; then
      echo "❌ FAIL: guard rejected ${label} (good) but output did not mention '${needle}'" >&2
      echo "${out}" >&2
      FAIL_COUNT=$((FAIL_COUNT + 1))
      return
    fi
  done
  echo "✓ guard correctly rejected ${label} (names the file and the key)"
}

expect_pass() {
  local label="$1"
  if bash "${GUARD}" >/tmp/guard_i18n_dupkey_test_out.$$ 2>&1; then
    echo "✓ guard correctly ignored ${label}"
  else
    echo "❌ FAIL: expected guard to ignore ${label} (false positive), but it rejected it" >&2
    cat /tmp/guard_i18n_dupkey_test_out.$$ >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
  rm -f /tmp/guard_i18n_dupkey_test_out.$$
}

# A locale file whose FIRST key (by en.json's own key order) is written
# twice in the raw text, with every other en.json key present exactly
# once -- so the parsed key SET (last-write-wins) is byte-identical to
# en.json's, and only check 9's raw-pair-list read can see the duplicate
# at all.
python3 - "${FIXTURE}" <<'PY'
import json
import sys

fixture_path = sys.argv[1]
data = json.load(open("web/locales/en.json"))
items = list(data.items())
dup_key, dup_val = items[0]
entries = [(dup_key, dup_val)] + items  # dup_key now appears twice

parts = [f"  {json.dumps(k)}: {json.dumps(v)}" for k, v in entries]
with open(fixture_path, "w") as f:
    f.write("{\n" + ",\n".join(parts) + "\n}\n")

print(f"planted duplicate key: {dup_key}", file=sys.stderr)
PY
DUP_KEY="$(python3 -c 'import json; print(list(json.load(open("web/locales/en.json")).items())[0][0])')"
expect_fail_containing "a locale file with a duplicated top-level key" \
  "duplicate key" "${FIXTURE}" "${DUP_KEY}"
rm -f "${FIXTURE}"

# Sanity: the guard must still pass clean on the real, unmodified tree
# (proves this test's own fixture and cleanup leave no residue behind).
expect_pass "the real, unmodified repository tree"

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "❌ ${FAIL_COUNT} guard-i18n_dupkey_test.sh case(s) failed" >&2
  exit 1
fi
echo "✓ guard-i18n_dupkey_test.sh: all cases passed"
