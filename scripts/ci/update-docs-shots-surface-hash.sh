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
# What it does: recomputes the per-file surface record (via
# guard-docs-shots.sh's own GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY mode, so
# this never duplicates the hash algorithm — see that script's header for
# why keeping a second copy in lockstep is exactly the drift risk to
# avoid) and rewrites ONLY the changed/added/removed entries of
# manifest.json's surface_files (ut-docs#6654; it also migrates the old
# single surface_sha256 key away). Every per-topic markdown hash and every
# screenshot PNG is left untouched — this is not a substitute for
# `make docs-shots` when a topic's own markdown changed, or when the
# surface change DOES affect a rendered page.
#
# Commit the resulting manifest.json diff (one 3-line entry per changed,
# added or removed surface file, nothing else) with a
# `Docs-Shots-Unchanged: true` trailer in your commit message. Nothing here
# reads or enforces that trailer — it is not a technical gate, since anyone
# able to run this script could already hand-edit the same entries — it
# exists purely so a reviewer scanning `git log` can tell a bare
# surface_files change was a deliberate, confirmed no-op refresh and not an
# accident. Because each file has its own entry, two PRs doing this for
# different files merge without a conflict; if two PRs add a file in the
# same sorted gap (or delete adjacent files), take either side of
# manifest.json and re-run this script.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

MANIFEST="web/help/img/manifest.json"
[ -f "${MANIFEST}" ] || {
  echo "update-docs-shots-surface-hash: ${MANIFEST} missing — run \`make docs-shots\` first" >&2
  exit 1
}

NEW_RECORD="$(GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1 bash scripts/ci/guard-docs-shots.sh)"

RESULT="$(python3 - "${MANIFEST}" "${NEW_RECORD}" <<'PY'
import json, sys
path, record = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as f:
    manifest = json.load(f)
new = {}
for line in record.splitlines():
    sha, rel = line.split("  ", 1)
    new[rel] = sha
old_raw = manifest.get("surface_files")
old = {p: (v.get("sha256") if isinstance(v, dict) else None) for p, v in old_raw.items()} if isinstance(old_raw, dict) else {}
if new == old and "surface_sha256" not in manifest:
    print(f"CURRENT {len(new)}")
    sys.exit(0)
changed = sorted(p for p in new if p in old and old[p] != new[p])
added = sorted(set(new) - set(old))
removed = sorted(set(old) - set(new))
manifest.pop("surface_sha256", None)
# Assigning an existing key keeps its position, and every unchanged entry
# serialises byte-for-byte as before, so the text diff is just the changed
# entries. A migrated manifest gets the key appended last, as lib.js writes it.
manifest["surface_files"] = {p: {"sha256": new[p]} for p in new}
with open(path, "w", encoding="utf-8") as f:
    # NOT sort_keys=True: that would reorder every topic's locale keys away
    # from the generator's own en/fa/ar/tr insertion order
    # (e2e/tests-docs/lib.js). Same bytes as JSON.stringify(m, null, 2) + "\n".
    json.dump(manifest, f, indent=2, ensure_ascii=False)
    f.write("\n")
print(f"surface_files updated: {len(changed)} changed, {len(added)} added, {len(removed)} removed")
for label, paths in (("changed", changed), ("added", added), ("removed", removed)):
    for p in paths:
        print(f"  - {label}: {p}")
PY
)"

if [[ "${RESULT}" == CURRENT* ]]; then
  echo "update-docs-shots-surface-hash: surface_files already matches (${RESULT#CURRENT } files) — nothing to do, guard-docs-shots.sh should already pass" >&2
  exit 0
fi

{
  echo "update-docs-shots-surface-hash: ${MANIFEST}"
  echo "${RESULT}"
} >&2
cat >&2 <<EOF

Next steps:
  1. Confirm 'git diff -- ${MANIFEST}' shows ONLY the entries listed above
     (one 3-line block per surface file).
  2. Commit it with a 'Docs-Shots-Unchanged: true' trailer, e.g.:
       git commit -m "\$(printf 'docs: refresh surface record (comment-only change)\n\nDocs-Shots-Unchanged: true\n')"
  3. Re-run 'bash scripts/ci/guard-docs-shots.sh' to confirm it now passes.
EOF
