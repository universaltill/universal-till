# Review — v0.31.1 release-note additions

- **Date:** 2026-10-08 · **Branch:** `docs/release-notes-v0.31.1-additions`
- **Built by:** Claude Opus 5.5 (lane:local, #3951's DevOps step) · **Reviewed by:** Claude Fable 5.1

## What shipped

Two owner-facing "New" lines in en/de/tr/fa/ar for changes that merged after the v0.31.1 notes (#1770) were written:
- plugin panels in the six core screens (ut-docs#3872);
- the device-information permission (ut-docs#3862, whose lane asked for this line before dispatch).

The Go guest SDK (ut-docs#3951) is developer-facing, so it has no owner line.

## Findings

Both lines were checked against the code: the slot hosts, the 2 s skip, and the device-info audit rows written before the value is handed over.
- fa used «گزارش حسابرسی»; the UI says «گزارش تغییرات». Fixed.
- de had a preposition with a bare list. Fixed.
- en now says "audit trail", the UI term. Fixed.
- tr and ar: no change.

`guard-release-notes.sh v0.31.1`, the compliance-claims guard and the competitor-naming guard pass.

## Added after review

universal-till#1771 (ut-docs#3873, the plugin photo-identify button on the sell screen) merged while this PR waited. One more "New" line was added in five languages. It reuses the UI's own `ai.identify.button` label in each language and the close-out's facts: the button shows only when a plugin hooks it, and it replaces the built-in one. It is mechanical in the same pattern, so there was no second review.

## Verdict

Safe to merge, then dispatch `release.yml -f bump=patch`.
