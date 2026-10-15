# Review: postinstall items symlink is NOT vestigial (ut-docs#256)

**Date:** 2026-10-15 · **Lane:** lane:local-mahshid76 · **Author model:** Sonnet 5.5 · **Reviewer:** Opus 5.5 (independent subagent)

## What shipped

The card asked to retire the `web/public/assets/items` symlink block in `packaging/scripts/postinstall.sh` as "vestigial". Verification showed it is not, so **no behaviour changed**. Instead:

- The stale comment ("shops upload item photos INTO web/public/assets/items") is rewritten to the real reason: the block bridges pre-2026-07-29 installs. `paths.migrateLegacyUploadedAssets` still reads the CWD-relative `web/public/assets/items` on every start (copy-if-absent) and the `/public/` handler still serves from that tree as its second source.
- `packaging/postinstall_items_test.go` pins both the symlink step (non-comment lines) and the explanation, so a future cleanup has to retire the migration first.
- Retiring the block is a separate follow-up card.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Comment said "first start after an upgrade"; the migration runs on every start (no first-run marker) | Fixed: "on every start (copy-if-absent)" |
| 2 | minor | Comment omitted that the `/public/` handler also serves from this tree, and that the in-app self-update swaps in a real `items` dir without the symlink (photos stay safe in `/var/lib/unitill/items` until the next .deb postinstall) | Fixed: both stated |
| 3 | minor | "demo thumbs seeded into an empty store only" is true of the script's `cp`, but dpkg may also write the packaged files through the symlink | Fixed: reworded to what the script itself does ("copied into /var/lib only when it is empty") |

Verified by the reviewer: diff is comment-only (`sh -n` OK); test fails against the original script for the right reasons (stale phrase, missing names) and fails when the `ln -sfn` line is deleted; no docs-shots surface file touched.

## Not verified

- A real `dpkg -i` upgrade over a symlinked `web/` (author's own certainty ~70% that dpkg keeps the symlink). This is why the block stays; the follow-up card must test it before any removal.
