#!/usr/bin/env bash
#
# Escape hatch for guard-docs-shots.sh's whole-file surface hashing
# (ut-docs#2102): a comment-only edit to a file under web/ui/**,
# web/public/** or internal/pages/**.go trips the guard for zero pixel
# change, and the only remedy until now was a full `make docs-shots` run —
# regenerating all 104 screenshots for a change that alters none of them,
# which conflicts with every other open PR by construction (that
# regeneration rewrites the whole manifest + PNG set).
#
# Use this ONLY after manually confirming the change you are about to
# commit cannot alter any rendered pixel (a comment, a doc string, dead
# code behind a flag never reached by a screenshotted page, etc). It does
# NOT verify that for you — see guard-docs-shots.sh's "ESCAPE HATCH" note
# for why an automated comment/real-change classifier was judged too
# risky to build. If you are at all unsure, run `make docs-shots` instead.
#
# What it does: recomputes ONLY the surface_sha256 field (via
# guard-docs-shots.sh's own GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY mode, so
# this never duplicates the hash algorithm — see that script's header for
# why keeping a second copy in lockstep is exactly the drift risk to
# avoid) and rewrites that one field into web/help/img/manifest.json.
# Every per-topic markdown hash and every screenshot PNG is left
# untouched — this is not a substitute for `make docs-shots` when a
# topic's own markdown changed, or when the surface change DOES affect a
# rendered page.
#
# Commit the resulting manifest.json diff (it will be a one-line hash
# change, nothing else) with a `Docs-Shots-Unchanged: true` trailer in
# your commit message. Nothing here reads or enforces that trailer — it
# is not a technical gate, since anyone able to run this script could
# already hand-edit the same field — it exists purely so a reviewer
# scanning `git log` can tell a bare surface_sha256 bump was a deliberate,
# confirmed no-op refresh and not an accident.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MANIFEST="web/help/img/manifest.json"
[ -f "${MANIFEST}" ] || {
  echo "update-docs-shots-surface-hash: ${MANIFEST} missing — run \`make docs-shots\` first" >&2
  exit 1
}

NEW_HASH="$(GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1 bash scripts/ci/guard-docs-shots.sh)"

OLD_HASH="$(python3 -c "import json; print(json.load(open('${MANIFEST}')).get('surface_sha256', ''))")"

if [ "${NEW_HASH}" = "${OLD_HASH}" ]; then
  echo "update-docs-shots-surface-hash: surface_sha256 already matches (${NEW_HASH:0:12}…) — nothing to do, guard-docs-shots.sh should already pass" >&2
  exit 0
fi

python3 - "${MANIFEST}" "${NEW_HASH}" <<'PY'
import json, sys
path, new_hash = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as f:
    manifest = json.load(f)
manifest["surface_sha256"] = new_hash
with open(path, "w", encoding="utf-8") as f:
    # NOT sort_keys=True: that would reorder every topic's locale keys away
    # from the generator's own en/fa/ar/tr insertion order
    # (e2e/tests-docs/lib.js), turning this into a 60+-line diff instead
    # of the one-line hash change this tool exists to produce.
    json.dump(manifest, f, indent=2, ensure_ascii=False)
    f.write("\n")
PY

cat >&2 <<EOF
update-docs-shots-surface-hash: ${MANIFEST}'s surface_sha256 updated
  ${OLD_HASH:0:12}… -> ${NEW_HASH:0:12}…

Next steps:
  1. Confirm 'git diff -- ${MANIFEST}' shows ONLY this one field changing.
  2. Commit it with a 'Docs-Shots-Unchanged: true' trailer, e.g.:
       git commit -m "\$(printf 'docs: refresh surface hash (comment-only change)\n\nDocs-Shots-Unchanged: true\n')"
  3. Re-run 'bash scripts/ci/guard-docs-shots.sh' to confirm it now passes.
EOF
