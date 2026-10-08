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

## Verdict

Safe to merge, then dispatch `release.yml -f bump=patch`.
