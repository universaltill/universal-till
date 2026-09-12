#!/usr/bin/env bash
#
# Regression test for scripts/ci/update-docs-shots-surface-hash.sh
# (ut-docs#2102): the cheap escape hatch that refreshes ONLY
# web/help/img/manifest.json's surface_sha256 field, without a full
# `make docs-shots` screenshot regeneration, for a surface edit an author
# has manually confirmed changes no rendered pixel.
#
# Everything this test mutates (manifest.json, one topic markdown, one
# planted fixture file, its own scratch files) is restored/removed from a
# single cleanup() trap, so a failure partway through never leaves the
# working tree dirty -- independent review (ut-docs#2102) caught an
# earlier draft of this trap missing the topic-markdown restore.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-docs-shots.sh"
UPDATER="scripts/ci/update-docs-shots-surface-hash.sh"
MANIFEST="web/help/img/manifest.json"
FIXTURE="internal/pages/zz_update_surface_hash_test_fixture.go"
FAIL_COUNT=0

MANIFEST_BACKUP="$(mktemp)"
PRE_UPDATE_BACKUP="$(mktemp)"
OUT="$(mktemp)"
cp "${MANIFEST}" "${MANIFEST_BACKUP}"

FIRST_TOPIC_MD="$(python3 -c "
import glob
print(sorted(glob.glob('web/help/en/*.md'))[0])
")"
TOPIC_MD_BACKUP="$(mktemp)"
cp "${FIRST_TOPIC_MD}" "${TOPIC_MD_BACKUP}"

cleanup() {
  local status=$?
  cp "${MANIFEST_BACKUP}" "${MANIFEST}"
  cp "${TOPIC_MD_BACKUP}" "${FIRST_TOPIC_MD}"
  rm -f "${MANIFEST_BACKUP}" "${PRE_UPDATE_BACKUP}" "${TOPIC_MD_BACKUP}" "${OUT}" "${FIXTURE}"
  exit "${status}"
}
trap cleanup EXIT

surface_field() {
  python3 -c "import json; print(json.load(open('${MANIFEST}')).get('surface_sha256',''))"
}

# --- baseline: real tree, manifest already fresh -> no-op, exit 0 -----
BEFORE="$(surface_field)"
if out="$(bash "${UPDATER}" 2>&1)"; then
  if grep -q "already matches" <<<"${out}"; then
    echo "✓ no-op when the manifest is already fresh"
  else
    echo "❌ FAIL: expected a no-op message on an already-fresh manifest, got:" >&2
    echo "${out}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
else
  echo "❌ FAIL: updater exited non-zero on an already-fresh manifest:" >&2
  echo "${out}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
AFTER="$(surface_field)"
if [[ "${AFTER}" != "${BEFORE}" ]]; then
  echo "❌ FAIL: no-op case still rewrote surface_sha256" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# --- plant a surface-changing fixture (no route -> must count as surface,
# same class guard-docs-shots_test.sh's "SharedHelperNoRoute" case covers) -
cat >"${FIXTURE}" <<'EOF'
package pages

func zzUpdateSurfaceHashTestHelper(n int) int {
	return n * 2
}
EOF

if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "❌ FAIL: guard should reject the planted fixture before any hash update, but it passed" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
elif grep -q "the app surface" "${OUT}"; then
  echo "✓ guard correctly rejects the planted fixture before the hash update"
else
  echo "❌ FAIL: guard rejected the fixture for the wrong reason:" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

cp "${MANIFEST}" "${PRE_UPDATE_BACKUP}"
if out="$(bash "${UPDATER}" 2>&1)"; then
  if grep -q "surface_sha256 updated" <<<"${out}"; then
    echo "✓ updater reports the hash was updated"
  else
    echo "❌ FAIL: updater exited 0 but did not report an update:" >&2
    echo "${out}" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
else
  echo "❌ FAIL: updater exited non-zero on a genuine surface change:" >&2
  echo "${out}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# Only surface_sha256 may differ, semantically (key-order-blind)...
if python3 - "${PRE_UPDATE_BACKUP}" "${MANIFEST}" <<'PY'
import json, sys
a = json.load(open(sys.argv[1]))
b = json.load(open(sys.argv[2]))
a_rest = {k: v for k, v in a.items() if k != "surface_sha256"}
b_rest = {k: v for k, v in b.items() if k != "surface_sha256"}
sys.exit(0 if a_rest == b_rest and a["surface_sha256"] != b["surface_sha256"] else 1)
PY
then
  echo "✓ only the surface_sha256 field changed semantically -- topics/other fields untouched"
else
  echo "❌ FAIL: the updater changed more than just surface_sha256 (or changed nothing)" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# ...AND textually: the whole point of this tool is a one-line diff, not
# just "no other key's VALUE changed" -- a key-order-blind check alone
# would miss a reordering pass (e.g. an accidental sort_keys=True) that
# turns this into a 60-line diff while still passing the check above.
DIFF_LINES="$(diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" | grep -c '^[+-][^+-]' || true)"
if [[ "${DIFF_LINES}" -eq 2 ]]; then
  echo "✓ the manifest diff is exactly one changed line (plus/minus), as documented"
else
  echo "❌ FAIL: expected a 1-line diff (2 diff-marker lines), got ${DIFF_LINES}:" >&2
  diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" >&2 || true
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "✓ guard passes after the surface-hash-only update, no screenshot regen needed"
else
  echo "❌ FAIL: guard still rejects after the hash update:" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
rm -f "${FIXTURE}"

# --- scope check: a genuinely stale TOPIC (markdown) must still fail --
# even right after a surface-hash-only refresh -- proves this tool cannot
# be used to paper over a real, unrelated staleness class.
printf '\n<!-- zz update-docs-shots-surface-hash test: unrelated topic edit -->\n' >>"${FIRST_TOPIC_MD}"

if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "❌ FAIL: guard should reject a stale topic markdown, but it passed" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
elif grep -q "topic markdown changed" "${OUT}"; then
  echo "✓ guard still rejects a genuinely stale topic after a surface-only refresh"
else
  echo "❌ FAIL: guard rejected the stale topic for the wrong reason:" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

bash "${UPDATER}" >"${OUT}" 2>&1 || true
if bash "${GUARD}" >"${OUT}" 2>&1; then
  echo "❌ FAIL: the surface-hash-only updater incorrectly cleared a topic-markdown staleness failure" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
else
  echo "✓ the surface-hash-only updater correctly leaves topic staleness for make docs-shots to fix"
fi

if [[ ${FAIL_COUNT} -gt 0 ]]; then
  echo "${FAIL_COUNT} failure(s)" >&2
  exit 1
fi
echo "✓ all update-docs-shots-surface-hash.sh regression cases passed"
