# Review: release notes v0.31.6

Date: 2026-10-10. Written by Opus 5.5 (lane:cloud-54); reviewed by Sonnet against every first-parent merge since v0.31.5.

## What shipped

Owner-facing notes in `web/release-notes/{en,de,ar,fa,tr}/v0.31.6.md`. The release was due after five feat/fix merges since v0.31.5:
- plugin HTTP header wait follows a job's deadline (#1827);
- identify-confirmed event to the identifying plugin (#1823);
- open-orders Move table toggle stays put (#1821);
- language-pack chip wording (#1813);
- additional till's Software update section (#1825) and Tab trapped in older dialogs (#1824), which landed while this PR was open; added after a rebase.

Left out as not owner-visible yet: #1817 (background-removal capability and endpoint; the picker UI is a later card) and #1832 (a plugin read view, developer-only).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 0 | Medium (self) | "parked orders" isn't the UI's term; tr button label differed from `basket.table.move` | **Fixed** before review: "Open orders" and the exact button labels in all five |
| 1 | Low | #1823 bullet overclaimed: the plugin is told only if it hooks the event | **Fixed** in all five ("if the plugin has asked to hear about it") |
| 2 | Low | #1827 bullet gave no ceiling | **Fixed** in all five ("up to five minutes") |
| 3 | Medium | ar: deposit called "التأمين", not the UI's "الوديعة" (#1824 bullet; second Sonnet pass) | **Fixed** |
| 4 | Low | tr "Yazılım güncelleme" vs locale "Yazılım güncellemesi"; ar "تلقائياً" spelling; fa "اضافه" as in help | **Fixed** |
| 5 | Low | ar till wording differs from help ("الجهاز") | Accepted: consistent with earlier release notes |

Terminology checked against `web/locales/*.json` and `web/help/*`: Open orders, Move table, Settings → Data.

## Verification

`scripts/ci/guard-release-notes.sh v0.31.6` ok; `go test ./internal/releasenotes` ok; compliance and competitor-naming guards ok.

## Verdict

Safe to merge, then release v0.31.6.
