# Review: release notes v0.34.20

- **Date:** 2026-10-26 · **Lane:** lane:cloud-24 · **Author:** Opus · **Reviewer:** Sonnet (different model), fresh context.
- **Covers:** owner-facing notes (en, de, fa, ar, tr) for the merges since v0.34.19: restore from a file, re-join keeps unsent sales, restore keeps the till's own cloud link, desktop "could not start" page, phone menu Lock alignment. Left out as developer/internal work: channel SDK truncated-redact field, payment authorize currency, a test-only fix.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | ar: "ذلك" ambiguous in the after-setup sentence | Fixed ("خيار الاستعادة هذا") |
| 2 | nit | fa: match the till's own words (فلش, پوشهٔ Downloads) | Fixed |
| 3 | nit | desktop "could not start" could sit under Fixed | Kept under Improved (new behaviour, not a regression fix) |
| 4 | nit | could also mention the "restore was not applied" notice | Not added; keeps the note short |

Every English bullet was fact-checked against the merged diffs and help topics; headings match each locale's previous notes. `guard-release-notes.sh v0.34.20` ok; `go test ./internal/releasenotes/...` ok; compliance and competitor-naming guards ok.

## Verdict

Safe to merge.
