# Review: release notes v0.34.13

- **Date:** 2026-10-20 · **Lane:** lane:cloud-41 · **Reviewer:** a different model, fresh context.
- **Covers:** the merges since v0.34.12 (ut-docs#4079, #4157, #2971, #4331, #4685, #3566, #4691), each checked against its card's close-out.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | must-fix | fa/ar "Cash" did not match the till's `tender.cash` label (fa نقدی, ar نقد) | Fixed |
| 2 | must-fix | "Persian and Arabic receipts" overclaimed (only the fa ZWNJ case shipped), and the ZWNJ was called a "joining mark" | Fixed in all five: Persian only, "half-space" |
| 3 | minor | Receipt designer labels left out the receipt number | Fixed |
| 4 | minor | Category filter scope is the Catalog and Inventory pages, not "item lists" | Fixed in all five |
| 5 | nit | fa colloquial verb, ar "جهاز الكاشير" vs "الصندوق", tr heading word order | Fixed |
| 6 | nit | Plugin settings could be "New" rather than "Improved" | Kept as Improved (a slice of an existing page) |

The end-of-day/Z report display-width fix (ut-docs#4689) is not merged and is not claimed.

`guard-release-notes.sh v0.34.13` ok; `go test ./internal/releasenotes/...` ok.

## Verdict

Safe to merge.
