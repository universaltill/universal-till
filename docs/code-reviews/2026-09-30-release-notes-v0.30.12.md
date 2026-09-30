# Review — release notes v0.30.12

**Date:** 2026-09-30 · **Branch:** `docs/release-notes-v0.30.12`

## What shipped
Owner-facing notes for v0.30.12 in en/de/ar/fa/tr (`web/release-notes/*/v0.30.12.md`). One entry under *Fixed*: the iOS on-screen keyboard no longer closes after each letter (ut-docs#3274, universal-till#1545). The other merges since v0.30.11 (#1541 salon install lock, #1542 SquareTile, #1543 plugin provides/markets) change nothing a shop owner sees, so they get no note.

## Checks
- `bash scripts/ci/guard-release-notes.sh v0.30.12`: ok.
- Section headings reuse each locale's existing *Fixed* heading. Translations checked against `reference/translation.md`: product names (iPhone, iPad, iOS) untouched, no card or PR numbers, same meaning as English.

## Verdict
Docs only. Safe to merge.
