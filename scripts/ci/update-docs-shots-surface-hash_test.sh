#!/usr/bin/env bash
#
# Regression test for scripts/ci/update-docs-shots-surface-hash.sh
# (ut-docs#2102): the cheap escape hatch that refreshes ONLY
# web/help/img/manifest.json's surface_files record (surface_sha256 before
# ut-docs#6654), without a full
# `make docs-shots` screenshot regeneration, for a surface edit an author
# has manually confirmed changes no rendered pixel.
#
# Everything this test mutates (manifest.json, one topic markdown, one
# planted fixture file, its own scratch files) is restored/removed from a
# single cleanup() trap, so a failure partway through never leaves the
# working tree dirty -- independent review (ut-docs#2102) caught an
# earlier draft of this trap missing the topic-markdown restore.
set -euo pipefail

# Fixtures are planted in a throwaway copy of what the guard reads, never
# the real tree (ut-docs#4311).
# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox web/ui web/public web/help internal/pages

GUARD="scripts/ci/guard-docs-shots.sh"
UPDATER="scripts/ci/update-docs-shots-surface-hash.sh"
MANIFEST="web/help/img/manifest.json"
FIXTURE="internal/pages/zz_update_surface_hash_test_fixture.go"
FAIL_COUNT=0

MANIFEST_BACKUP="$(mktemp)"
PRE_UPDATE_BACKUP="$(mktemp)"
OUT="$(mktemp)"
cp "${MANIFEST}" "${MANIFEST_BACKUP}"

#
# Must be a topic the guard actually ROUTES (has non-empty `routes:` front
# matter) -- NOT just the alphabetically-first *.md file. A reference-only
# topic (no `routes:`, reached only via helpLink from another page, e.g.
# age-restricted-sales.md) is correctly invisible to the guard's staleness
# check, so appending to one would never produce "topic markdown changed"
# and this test would misreport the (unrelated) surface-staleness message
# below as a failure (ut-docs#3340 review caught this when such a topic's
# filename happened to glob-sort first). Mirrors guard-docs-shots.sh's own
# routed_topics_with_routes() front-matter parse -- keep in lockstep.
FIRST_TOPIC_MD="$(python3 -c "
import glob, re
for path in sorted(glob.glob('web/help/en/*.md')):
    text = open(path, encoding='utf-8').read().replace('\r\n', '\n')
    if not text.startswith('---\n'):
        continue
    end = text.find('\n---', 4)
    if end < 0:
        continue
    routes = None
    for line in text[4:end].split('\n'):
        m = re.match(r'^([A-Za-z_]+):\s*(.*)\$', line.strip())
        if not m:
            continue
        if m.group(1) == 'routes':
            routes = [s.strip().strip('\"\\'') for s in m.group(2).lstrip('[').rstrip(']').split(',') if s.strip().strip('\"\\'')]
    if routes:
        print(path)
        break
")"
TOPIC_MD_BACKUP="$(mktemp)"
cp "${FIRST_TOPIC_MD}" "${TOPIC_MD_BACKUP}"

cleanup() {
  local status=$?
  cp "${MANIFEST_BACKUP}" "${MANIFEST}"
  cp "${TOPIC_MD_BACKUP}" "${FIRST_TOPIC_MD}"
  rm -f "${MANIFEST_BACKUP}" "${PRE_UPDATE_BACKUP}" "${TOPIC_MD_BACKUP}" "${OUT}" "${FIXTURE}"
  remove_guard_test_sandbox
  exit "${status}"
}
trap cleanup EXIT

# The per-file record (ut-docs#6654), as a stable text form to compare.
surface_field() {
  python3 -c "import json; print(json.dumps(json.load(open('${MANIFEST}')).get('surface_files', {}), sort_keys=True))"
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
if [[ "${AFTER}" == "{}" ]]; then
  echo "❌ FAIL: the manifest has no surface_files record" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
if [[ "${AFTER}" != "${BEFORE}" ]]; then
  echo "❌ FAIL: no-op case still rewrote surface_files" >&2
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
  if grep -q "surface_files updated" <<<"${out}"; then
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

# Only surface_files may differ, semantically (key-order-blind)...
if python3 - "${PRE_UPDATE_BACKUP}" "${MANIFEST}" <<'PY'
import json, sys
a = json.load(open(sys.argv[1]))
b = json.load(open(sys.argv[2]))
a_rest = {k: v for k, v in a.items() if k != "surface_files"}
b_rest = {k: v for k, v in b.items() if k != "surface_files"}
sys.exit(0 if a_rest == b_rest and a["surface_files"] != b["surface_files"] else 1)
PY
then
  echo "✓ only the surface_files record changed semantically -- topics/other fields untouched"
else
  echo "❌ FAIL: the updater changed more than just surface_files (or changed nothing)" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# ...AND textually: the whole point of this tool is a one-line diff, not
# just "no other key's VALUE changed" -- a key-order-blind check alone
# would miss a reordering pass (e.g. an accidental sort_keys=True) that
# turns this into a 60-line diff while still passing the check above.
PLUS_LINES="$(diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" | grep -c '^+[^+]' || true)"
MINUS_LINES="$(diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" | grep -c '^-[^-]' || true)"
if [[ "${PLUS_LINES}" -eq 3 && "${MINUS_LINES}" -eq 0 ]]; then
  echo "✓ a new surface file adds exactly its own 3-line entry (no other line changes)"
else
  echo "❌ FAIL: expected +3/-0 diff lines for one new surface file, got +${PLUS_LINES}/-${MINUS_LINES}:" >&2
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

# --- removed file: its entry (3 lines) goes, nothing else changes --------
cp "${MANIFEST}" "${PRE_UPDATE_BACKUP}"
if out="$(bash "${UPDATER}" 2>&1)" && grep -q "1 removed" <<<"${out}" &&
  [[ "$(diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" | grep -c '^-[^-]' || true)" -eq 3 ]] &&
  [[ "$(diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" | grep -c '^+[^+]' || true)" -eq 0 ]]; then
  echo "✓ a removed surface file drops exactly its own entry"
else
  echo "❌ FAIL: removing the planted file did not drop exactly its 3-line entry:" >&2
  echo "${out:-}" >&2
  diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" >&2 || true
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# --- comment-only edit to an EXISTING surface file (AC5) ----------------
# Capture first, then filter: `| head -1` under pipefail can SIGPIPE (141).
SURFACE_REC="$(GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1 bash "${GUARD}")"
EXISTING="$(awk '$2 ~ /^web\/public\// {print $2; exit}' <<<"${SURFACE_REC}")"
EXISTING_BACKUP="$(mktemp)"
cp "${EXISTING}" "${EXISTING_BACKUP}"
printf '\n/* zz update-docs-shots-surface-hash test: comment-only edit */\n' >>"${EXISTING}"
cp "${MANIFEST}" "${PRE_UPDATE_BACKUP}"
bash "${UPDATER}" >"${OUT}" 2>&1 || true
DIFF="$(diff -u "${PRE_UPDATE_BACKUP}" "${MANIFEST}" || true)"
if [[ "$(grep -c '^[+-][^+-]' <<<"${DIFF}")" -eq 2 ]] &&
  python3 - "${MANIFEST}" "${EXISTING}" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
# the changed sha line is the one under that file's own key
import re
lines = open(sys.argv[1], encoding="utf-8").read().split("\n")
i = lines.index('    "%s": {' % sys.argv[2])
sys.exit(0 if '"sha256"' in lines[i + 1] and sys.argv[2] in m["surface_files"] else 1)
PY
then
  echo "✓ a comment-only edit changes exactly one line, under that file's own key"
else
  echo "❌ FAIL: a comment-only edit should change exactly one sha line:" >&2
  echo "${DIFF}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
if bash "${GUARD}" >"${OUT}" 2>&1 && out="$(bash "${UPDATER}" 2>&1)" && grep -q "already matches" <<<"${out}"; then
  echo "✓ guard passes after the refresh and a second updater run is a no-op"
else
  echo "❌ FAIL: guard or second-run no-op failed after the comment-only refresh" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi
cp "${EXISTING_BACKUP}" "${EXISTING}"
rm -f "${EXISTING_BACKUP}"
cp "${MANIFEST_BACKUP}" "${MANIFEST}"

# --- legacy manifest: the updater migrates surface_sha256 -> surface_files --
python3 - "${MANIFEST}" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
del m["surface_files"]
m["surface_sha256"] = "0" * 64
json.dump(m, open(sys.argv[1], "w"), indent=2, ensure_ascii=False)
open(sys.argv[1], "a").write("\n")
PY
if bash "${UPDATER}" >"${OUT}" 2>&1 && bash "${GUARD}" >"${OUT}" 2>&1 &&
  ! grep -q surface_sha256 "${MANIFEST}"; then
  echo "✓ the updater migrates a legacy surface_sha256 manifest"
else
  echo "❌ FAIL: legacy manifest not migrated:" >&2
  cat "${OUT}" >&2
  FAIL_COUNT=$((FAIL_COUNT + 1))
fi

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
