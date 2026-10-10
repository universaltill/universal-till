# Review: release notes v0.31.6

Date: 2026-10-10. Written by Opus 5.5 (lane:cloud-54); reviewed by Sonnet against every first-parent merge since v0.31.5.

## What shipped

Owner-facing notes in `web/release-notes/{en,de,ar,fa,tr}/v0.31.6.md`. The release was due after five feat/fix merges since v0.31.5:
- plugin HTTP header wait follows a job's deadline (#1827);
- identify-confirmed event to the identifying plugin (#1823);
- open-orders Move table toggle stays put (#1821);
- language-pack chip wording (#1813).

Left out as not owner-visible yet: #1817 (background-removal capability and endpoint; the picker UI is a later card).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 0 | Medium (self) | "parked orders" isn't the UI's term; tr button label differed from `basket.table.move` | **Fixed** before review: "Open orders" and the exact button labels in all five |
| 1 | Low | #1823 bullet overclaimed: the plugin is told only if it hooks the event | **Fixed** in all five ("if the plugin has asked to hear about it") |
| 2 | Low | #1827 bullet gave no ceiling | **Fixed** in all five ("up to five minutes") |

Terminology checked against `web/locales/*.json` and `web/help/*`: Open orders, Move table, Settings → Data.

## Verification

`scripts/ci/guard-release-notes.sh v0.31.6` ok; `go test ./internal/releasenotes` ok; compliance and competitor-naming guards ok.

## Verdict

Safe to merge, then release v0.31.6.
