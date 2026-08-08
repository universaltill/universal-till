# Code review: export payload row-count bound

**Ticket:** universaltill/ut-docs#439
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/export-row-size-bound-439`
**Reviewer:** independent Sonnet subagent (complexity:easy tier), fresh context, isolated worktree

## What shipped

`POST /api/data/export`'s existing 366-day range cap (ut-docs#229) only
bounds elapsed *time*, not row count or payload size — 366 days of a busy
till's sales can still be six-figure rows loaded into one in-memory slice
and marshalled whole into the WASM guest's stdin on Pi-class hardware.

- Added `POSRepo.CountSalesForExport` (`internal/data/export_repo.go`) — a
  cheap `SELECT COUNT(*)` sharing its WHERE clause with `SalesForExport`
  via a new `exportToSentinel` helper (extracted from `SalesForExport`'s
  own date-only inclusive-final-day sentinel logic), so the two queries can
  never silently disagree about which sales match.
- Added `maxExportSalesRows` (`internal/pages/data_api.go`, a `var` not a
  `const` so tests can override it instead of seeding 50,000 rows) —
  50,000, generous for a normal month/quarter/year-end export while ruling
  out the six-figure case the range cap alone can't catch.
- `POST /api/data/export`'s handler now calls `CountSalesForExport` and
  rejects with 400 *before* calling `SalesForExport`'s batch gather (and
  the WASM plugin dispatch after it) when the matched count exceeds the
  bound — same "reject before doing the expensive work" shape the range
  cap already uses. Only runs when `sales:read` is granted (`hasSales`);
  no sales are gathered otherwise, so nothing to bound.

### Tests (written test-first, TDD)

- `TestCountSalesForExport` / `TestCountSalesForExport_EmptyRange`
  (`internal/data/export_repo_test.go`) — reuses `TestSalesForExport`'s
  exact fixture (in-range, boundary-day, before/after range, an
  in-range return excluded by `sale_type`) and cross-checks
  `CountSalesForExport`'s result against `len(SalesForExport(...))` on the
  same data, so the two can't drift apart unnoticed.
- `TestExportDispatch_RowCountTooLarge` / `TestExportDispatch_RowCountAtCap`
  (`internal/pages/export_dispatch_test.go`) — override `maxExportSalesRows`
  to 2, seed 3 vs. 2 matching sales; confirms rejection above the cap and
  success exactly at it (bound inclusive, mirroring
  `TestExportDispatch_DateRangeAtCap`'s boundary convention). Subscribes an
  export handler first so a broken/missing cap would 200, not just 400 for
  an unrelated reason (same red-herring caution the existing
  `TestExportDispatch_DateRangeTooLarge` already uses).

## Independent review

An independent Sonnet subagent, fresh context (never saw the dev
reasoning), isolated in its own git worktree:

- Ran `go build`, `go vet`, `gofmt -l` on the touched files, the targeted
  `TestSalesForExport`/`TestCountSalesForExport` and `TestExportDispatch`
  suites, the full `go test ./...` (green except the known, unrelated
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`,
  ut-docs#415, a pre-existing root-sandbox artifact), and all three CI
  guards (`guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`)
  — all green.
- **Independently re-verified the TDD claim**: removed just the new
  count-check block from the handler (kept both new tests), reproduced
  `TestExportDispatch_RowCountTooLarge` failing with a real error
  (`expected 400, got 200`) — a 3-row match slipping through a 2-row cap —
  confirmed `RowCountAtCap` still trivially passes either way (expected,
  it's the success-path case), then restored the fix and confirmed both
  pass again with a clean working tree.
- Confirmed `CountSalesForExport` and `SalesForExport` share byte-identical
  WHERE predicates via `exportToSentinel`, verified against
  `TestCountSalesForExport`'s explicit `got == len(rows)` cross-check.
- Confirmed the count check is placed strictly before `SalesForExport`'s
  batch gather, only under `hasSales`, and the bound is inclusive
  (`count > maxExportSalesRows`), consistent with the existing range cap's
  `>` semantics.
- Confirmed the shared package-level `maxExportSalesRows` var is safe to
  mutate in tests: neither `internal/data` nor `internal/pages` uses
  `t.Parallel()`, so no concurrent read/write race.
- Confirmed the new rejection message follows this handler's own existing
  convention of plain (non-`T`-templated) JSON `message` strings — already
  out of `guard-i18n.sh`'s scope, matching every sibling message in this
  handler (`"manager only"`, the range-cap message, etc.); `guard-i18n.sh`
  stayed green, confirming no new inconsistency.
- Confirmed no `os.MkdirAll`/cwd-relative-path bug class applies (diff
  writes no files).
- Confirmed no real client/shop name or secret-shaped literal in any new
  test data.
- Confirmed no manual/help-topic update needed: no new route, the JSON
  `message` field isn't templated UI text, `guard-help-topics.sh` green.

### Findings

One observation, not a defect: the doc comment's own worked example
(~135 sales/day × 366 days ≈ 49,410) leaves only ~590 rows of headroom
under the 50,000-row cap for the exact "very busy till, full year"
scenario it uses to justify the number. Self-consistent, not a bug, and
`maxExportSalesRows` is a plain adjustable `var` if that scenario turns
out to be real and growing — left as-is rather than papered over with a
rounder, less-honest number.

No blocker-class issue (money/tax, data loss, security) — no second
review round.

## Verification performed (this session, after independent review)

- `go build ./...`, `go vet ./...`, `gofmt -l` on all 4 touched files —
  clean.
- `go test ./internal/data/... -run 'TestSalesForExport|TestCountSalesForExport' -v` — 6/6 pass.
- `go test ./internal/pages/... -run TestExportDispatch -v` — 20/20 pass.
- `go test ./...` — every package passes except the pre-existing,
  unrelated `TestSaveCleansUpDirectoryOnWriteFailure` (ut-docs#415).
- `go test ./internal/data/... ./internal/pages/... -race -run 'TestSalesForExport|TestCountSalesForExport|TestExportDispatch|TestStockForExport'` — clean, no races.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh` — all green.
- TDD claim re-verified personally as well as by the independent
  reviewer: reverted the count-check block, confirmed
  `TestExportDispatch_RowCountTooLarge` fails with `expected 400, got 200`,
  restored the fix, confirmed green again.

## Scope

`internal/data/export_repo.go` (new `CountSalesForExport` +
`exportToSentinel` helper, `SalesForExport` refactored to share it),
`internal/pages/data_api.go` (row-count bound on
`POST /api/data/export`), plus regression tests in both packages. No
migrations, no new page routes, no template/locale changes, no
plugin-loading/verification path touched.

## Outcome

Independent review found no blocking issues; the row bound is placed
correctly (before the expensive gather), the count query provably agrees
with the data query it bounds, and the boundary is inclusive and
correctly tested in both directions. One non-blocking observation on the
cap's headroom left as a documented, adjustable `var` rather than
"fixed" into a false sense of precision.

Safe to merge.
