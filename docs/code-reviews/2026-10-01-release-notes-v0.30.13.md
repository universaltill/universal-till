# Review — release notes v0.30.13

**Date:** 2026-10-01 · **Branch:** `docs/release-notes-v0.30.13`

## What shipped
Owner-facing notes for v0.30.13 in en/de/ar/fa/tr (`web/release-notes/*/v0.30.13.md`), all under *Fixed*:
- the phone layout sweep (ut-docs#3297, universal-till#1551)
- the Report-an-issue tile and settings card following the issue-reporting permission (ut-docs#3135, #1544)
- plugin events no longer dropped during a long import/export on phones and tablets (ut-docs#3171, #1548)

## Checks
- `bash scripts/ci/guard-release-notes.sh v0.30.13`: ok.
- Headings reuse each locale's existing *Fixed* heading. UI names quoted in bold match each locale's own strings (`issuereport.nav_label`, the dine-in/takeaway labels; de from the language pack). Product names untouched, no card or PR numbers.

## Verdict
Docs only. Safe to merge.
