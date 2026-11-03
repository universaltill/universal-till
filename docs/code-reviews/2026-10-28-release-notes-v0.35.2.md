# Review: release notes v0.35.2

Date: 2026-10-28 · Docs-only change (web/release-notes/{en,de,ar,fa,tr}/v0.35.2.md)

## Scope check

Notes list only what `git log --first-parent v0.35.1..origin/main` contains:
item icon on the basket line and refresh of an open basket line (#2201) and
the crash-safe backup-restore swap (#2182).

The sales-copy PRs (#2169, #2171, #2172, #2174, #2175, #2176, #2179) are
still open and are not mentioned.

## Checks

- Plain Improved/Fixed, no PR or card numbers; guard-release-notes.sh ok.
- Translations done by the cycle model against the v0.35.1 glossary
  (till = Kasse / kasa / صندوق); structure identical across the five locales.
