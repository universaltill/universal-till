# Review — release notes v0.31.0

**Date:** 2026-10-05 · **Author/reviewer:** Opus 5.5 orchestrator (docs-only, no code)

## What shipped
Owner-facing notes for v0.31.0 in en/de/ar/fa/tr. Sources are the close-outs of the Done cards merged since v0.30.19:
- the guided tour (#3710);
- the import welcome page and the shorter setup wizard (#3709);
- the unpaid start-up check-in (#3673);
- the subscription "paid until" wording (#3461);
- promotions on a phone-sized till (#3653).

Left out:
- #3383 and #3652: my. cloud changes, not shipped in the till binary;
- #3466: still open, awaiting an Android device check;
- #3637: internal, with no visible change;
- #2905: test only.

## Checks
- `guard-release-notes.sh v0.31.0` ok; release-notes Go tests ok.
- Button names quoted in the notes match each locale's `tour.*` keys: en Skip tour, de Tour überspringen, ar تخطي الجولة, tr Turu atla, fa رد کردن راهنما.
- No card numbers in the notes.

## Verdict
Safe to merge.

## Independent check
Sonnet 5.5 read-only review: no blocking issues. Applied: "signed-in" in en; German quotes both button names.
