# Code review: scope xlsx merged-cell rejection to header+data rectangle (ut-docs#1853)

**Date:** 2026-09-09
**Card:** ut-docs#1853 — "catimport: scope xlsx merged-cell rejection to the
header+data rectangle, not the whole sheet" (follow-up from the
`universal-till` PR #939 / ut-docs#1837 review)
**Branch:** `fix/1853-xlsx-merge-scope`
**Diff:** `internal/catimport/xlsx.go`, `internal/catimport/xlsx_test.go`,
`web/help/{en,de,fa,tr,ar}/catalog.md`
**Reviewer:** independent (Opus, fresh context, different model from the
Sonnet implementation, did not write the code)

## What shipped

`ParseXLSX`'s merged-cell rejection (`ErrXLSXMergedCells`) used to reject the
whole workbook if `GetMergeCells` returned ANY merge anywhere on the sheet.
That was over-broad: a purely decorative merge (a company-logo cell off to
the side of the real columns, a trailing note or totals row) rejected an
otherwise perfectly-importable real German merchant export.

The fix narrows rejection to only a merge that overlaps **both** (a) a row
that would otherwise import as a real catalog item (the header row, or a
data row with a non-blank name and a readable price), **and** (b) a column
`headerIndex` actually recognised (`rejectMergedCells`,
`dataRectangleLastRow`, `dataRectangleColumns`, `mergeOverlapsRectangle`). A
merge inside that rectangle still rejects, unchanged
(`TestParseXLSX_MergedCells`).

## Independent review — one blocker found and fixed before merge

The first draft bounded the row range at the **first wholly blank row**
after the header, treating everything past it as "not real data". That was
wrong, and dangerous: `ParseXLSX`'s import loop only `continue`s on a blank
row — it does not stop scanning. A blank separator row legitimately
resumes with more real items afterwards
(`TestParseXLSX_BlankSeparatorRowSkipped` already covers this shape), and a
merge on one of those later rows escaped the first draft's check entirely.
Concretely: a header (Name/Price/Tax rate), a clean row, a blank separator,
then two more clean rows — a merge spanning Price+Tax on the last of those
rows silently blanked the **tax rate** (no `Issue` is raised for a blanked
tax cell, unlike a blanked name/price) while `ParseXLSX` reported success.
Before this change the whole file would have been rejected; the first
draft of the fix would have silently imported a wrong VAT rate — precisely
the outcome `ErrXLSXMergedCells` exists to prevent, on a compliance-
sensitive field in the Germany pilot.

**Fix:** `dataRectangleLastRow` now scans every row (gaps included) and
tracks the last row that would import as a **clean item** — non-blank name
AND a price that actually parses — wherever it falls, not the first gap.
This still lets the ticket's own motivating case through (a trailing note
whose row has no readable price never counts as clean, so it never extends
the protected range), while now correctly protecting a legitimate second
block of data resuming after a separator row.

Verified TDD-style: reverted just this fix (restored the first-blank-row-
stops logic) and reran the new regression test —
`TestParseXLSX_MergeAfterBlankSeparatorRowStillRejects` failed (`err = <nil>`,
wanted `ErrXLSXMergedCells`) against the reverted code and passes against
the fix.

### Other findings addressed

- **Error precedence for a merged title-row-as-header.** The merge check
  now runs after header recognition (it needs `idx` to build the column
  span), so a workbook whose header row is actually a merged title banner
  reports `ErrNoNameColumn`, not `ErrXLSXMergedCells`. Confirmed this is
  the *more* consistent outcome, not a regression: the unmerged case
  (`TestParseXLSX_LeadingTitleRow`) already reports `ErrNoNameColumn`, so a
  title row now fails the same way whether or not it happens to be merged.
  Pinned with `TestParseXLSX_MergedTitleRowAsHeaderStillReportsNoNameColumn`
  and the doc comment on `rejectMergedCells` calls this out explicitly.
- **Missing boundary/multi-merge/partial-overlap coverage.** Added
  `TestParseXLSX_MergeOnLastCleanDataRowRejects` (the off-by-one this
  helper could easily regress into), `TestParseXLSX_OnlyOneOfSeveralMergesOverlappingStillRejects`
  (a harmless merge checked first must not mask a real one), and
  `TestParseXLSX_PartiallyOverlappingColumnMergeRejects` (a merge that
  straddles from a recognised column into an unused one is still
  "overlapping", not "fully contained" — the column span is deliberately a
  range, not a precise set, so this is intentional over-rejection, not a
  bug). Also added small table tests directly against the two pure
  helpers, `TestMergeOverlapsRectangle` and `TestDataRectangleLastRow`, to
  pin the boundary arithmetic independent of `ParseXLSX`'s higher-level
  behaviour.

  Two of these three tests initially failed for a different reason than
  intended: merging the row's own Name+Price cell together blanks the
  *price* cell (Excel merge semantics: only the merge's top-left cell
  keeps its value), which makes that row fail its own "clean item" check
  and stop counting as protected — a self-referential edge case, not the
  boundary/multi-merge behaviour those tests were meant to pin. Redesigned
  both to merge Price+Tax or Price+Category instead (Price is the
  merge's top-left and survives; the *other* field silently blanks),
  which exercises the intended scenario without corrupting the row's own
  name/price.
- **Nits (style, not correctness):** `dataRectangleColumns` renamed its
  named returns from `min`/`max` (shadowing the Go 1.21+ builtins) to
  `minCol`/`maxCol`, dropped a pointless `if minVal := col; ...` binding,
  and now fails **closed** (protects every column, `[1, math.MaxInt]`)
  rather than open if `idx` were ever empty — belt-and-braces, since the
  caller already guarantees `idx["name"]` exists before this runs.
- **Help text wording.** The English (and de/fa/tr/ar) catalog help topic
  said a decorative merge "off to the side" or "far below your data"
  doesn't block the import; tightened to "outside those columns" / "on a
  trailing note or totals row that isn't itself a readable item" — a merge
  between two recognised columns (not just past the last one) still
  blocks, and "far below" was never really the boundary (a gap of one
  blank row is enough, wherever it falls).

## Verified

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./...` (full repo suite) — all pass.
- `golangci-lint run ./...` — 0 issues.
- All 18 CI-blocking guards from `ci.yml`'s `build` job — pass, including
  `guard-docs-shots.sh` after `make docs-shots` regenerated the topic
  manifest for the five help-doc edits (no visual screen changed — the
  topic's own screenshot, `catalog.png`, is byte-identical in every
  locale; only the manifest's per-topic content hash moved). Two
  unrelated screenshots (`sell`, `multitill`) came back byte-different
  from the same `make docs-shots` run — reverted to `main`'s version
  rather than committed, since they're noise from re-running the full
  Playwright suite, not a real change this branch makes.
- TDD: every new/changed test verified to fail against the pre-fix code
  and pass against the fix, per test (see above for the blocker; the
  others were confirmed the same way while iterating).

## Not in scope

- This package (`internal/catimport`) predates the repo's `money.Money`
  convention and still uses raw `int64` minor units throughout
  (`ImportItem.PriceMinor`, `ParsePrice`'s return). Untouched by this
  diff, not a regression it introduces, and out of scope for a bugfix
  branch — flagged for awareness only.
