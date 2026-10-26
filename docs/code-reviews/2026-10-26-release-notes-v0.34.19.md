# Review: release notes v0.34.19

- **Date:** 2026-10-26 · **Lane:** lane:local · **Reviewer:** a different model (Opus), fresh context.
- **Covers:** the Mac app launch fix and the localised Local Network prompt text, added to the existing v0.34.19 notes (en, de, tr, ar, fa).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | must-fix | "card readers" is not in the shipped prompt text (receipt printers and other tills only) | Fixed in all five |
| 2 | must-fix | The prompt text must be merged before the cut | Release waits for that PR |
| 3 | must-fix | fa used the Arabic spelling "برنامج" | Fixed |
| 4 | must-fix | ar used "الكاشير" for till and the wrong gender; other notes use "صندوق" | Fixed |
| 5 | minor | de "Netzwerkabfrage"; prompt language follows the Mac, not the till | Fixed in all five |
| 6 | nit | fa heading and title wording; tr title | Fixed |

`guard-release-notes.sh v0.34.19` ok.

## Verdict

Safe to merge.
