# Code review: SeasonalForecast's per-window queries collapsed into one scan

**Ticket:** universaltill/ut-docs#199
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/199-seasonal-forecast-single-scan`
**Reviewer:** independent Opus subagent (complexity:medium tier), isolated worktree

## What shipped

`POSRepo.SeasonalForecast` (`internal/data/pos_repo.go`) used to run one SQL
query **per historical window** — up to 6 (3 prior years × solar+lunar) —
each a full `sale_lines`/`sales`/`items` scan. Every window's predicate had
to wrap the column (`datetime(s.created_at) >= datetime('now', ?)`) because
`created_at` is stored RFC3339 (`...T...Z`) while `datetime('now', ...)`
returns SQLite's own space-separated format (the same trap `PeriodComparison`
documents a few hundred lines above); that wrapper defeats
`idx_sales_created` on every one of those 6 scans.

- Added `seasonalWindow` (one solar/lunar window descriptor) and
  `seasonalWindowQuery(windows, days) (string, []any)`, which builds ONE
  query: one `SUM(CASE WHEN <window bound> THEN sl.quantity ELSE 0 END)`
  column per requested window, gated by a `WHERE ... AND (<window1 bound> OR
  <window2 bound> OR ...)` so the join is scanned exactly once regardless of
  how many windows (1–6) are requested.
- `SeasonalForecast` now calls this once instead of looping
  `scanWindow`/`db.QueryContext` up to 6 times; the per-item accumulation
  (`acc.solar[k]`/`acc.lunar[k]`, the max(solar,lunar) de-dup, the
  category rollup) is unchanged.
- Datetime wrapping is unavoidable given the storage format (confirmed, not
  assumed — real `sales` rows can be either RFC3339 or SQLite's
  `datetime('now')` default format depending on write path), so this does
  not turn into an index search; it satisfies the ticket's other explicit
  acceptance option — "a single scan for all windows" — instead.

### Tests (written test-first, TDD)

- `TestSeasonalWindowQuery_IsSingleScanNotOnePerWindow` — a pure unit test
  (no DB) asserting the generated query text contains `FROM sale_lines`
  exactly once and the arg count matches `windows × 4`. Confirmed failing
  to compile pre-fix (`undefined: seasonalWindow`/`seasonalWindowQuery`).
- `TestSeasonalWindowQuery_PlanIsOneScan` — runs `EXPLAIN QUERY PLAN` on the
  real generated 6-window query against a migrated DB and asserts the
  `sale_lines` table (by its `sl` alias token, not a substring match — see
  review finding below) is scanned exactly once, the ticket's literal
  acceptance criterion.
- All 8 pre-existing behavioural `SeasonalForecast` tests
  (multi-year averaging, lunar-window shift, overlap de-dup, category
  rollup, variant stock, lunar boundary pinning, empty history, rounding)
  stay green unmodified — they're the regression net proving output is
  byte-identical to the old per-window behaviour.

## Independent review (round 1)

An independent Opus subagent, isolated in its own git worktree, reviewed
the diff without having seen any prior reasoning about it:

- Read the full diff and the surrounding `SeasonalForecast` function (not
  just the hunks). Ran `go build`, `go vet`, `gofmt -l`,
  `go test ./internal/data/... -run Seasonal -v`,
  `go test ./internal/data/... -race -count=1`, the full `go test ./...`
  (one pre-existing unrelated failure only, see below), and
  `guard-data-access.sh`/`guard-i18n.sh` — all green, exact output recorded
  in the review transcript.
- **Verified arg-ordering correctness empirically, not by reading**: seeded
  one sale per window with distinct quantities (`[11,22,33,44,55,66]`) and
  read the raw per-window sums back — matched hand-computed window geometry
  exactly, confirming the SELECT-CASE args and WHERE-OR args (each pass
  built separately, in query-text order) bind to the right placeholders.
- **Ran `EXPLAIN QUERY PLAN` independently** at 1/2/4/6-window counts and
  against a 400-row seeded, `ANALYZE`d DB — `sale_lines` scanned exactly
  once every time, real cost collapsing from "6 full scans + a correlated
  subquery per row, 6×" to "1 scan + indexed lookups downstream."
- **Independently re-verified the TDD claim**: reverted only
  `pos_repo.go`'s implementation (kept the new test file), confirmed the
  new tests fail to *compile* (`undefined: seasonalWindow`), restored, and
  confirmed `go test ./internal/data/... -run Seasonal` passes clean again
  — with the caveat that a compile failure is a weaker TDD signal than a
  red assertion, and that the real behavioural safety net is the 8
  pre-existing exact-value tests.
- Confirmed no SQL injection risk (every interpolated fragment is
  internally-generated literal SQL/`?` placeholders, never user input), no
  money-type concerns (quantities, not currency — correctly untouched), and
  zero i18n/UI/plugin/offline-first surface touched (2 files changed, both
  `internal/data`; `guard-i18n.sh` green).

### Findings

1. **Fixed in this round (real, if currently unreachable) — NaN could leak
   into a category rollup.** The old per-window queries used
   `HAVING units > 0`, so an item could never enter the result with an
   all-zero signal. The new `WHERE (<window ORs>)` gate only guarantees a
   matching *row* existed in some window, not that its quantity summed
   positive — a net-zero-quantity line still passes it, producing an
   all-zero-sums row. `firstK` then stays `0`, and
   `sum/float64(firstK)` = `0/0` = `NaN`, which poisons the item's *entire
   category* (`c.Expected += it.Expected`) on the live reports page.
   Traced every `sale_lines` writer: `validateLine` rejects `Qty <= 0`
   (`internal/pos/sales.go`) on every reachable path today, so this is
   latent, not live — but it's a defensive property the old code had and
   this diff silently dropped. **Fix**: `if firstK == 0 { continue }` added
   right after `firstK` is computed, restoring the old skip exactly.
   Regression test added:
   `TestPOSRepo_SeasonalForecast_ZeroSumWindowItemDoesNotPoisonCategoryWithNaN`
   — confirmed failing with the exact predicted NaN output when the guard
   is reverted, passing with it restored.
2. **Fixed in this round — `TestSeasonalWindowQuery_PlanIsOneScan` was
   planner-fragile.** It matched `strings.Contains(detail, "sale_lines")`,
   which only works by coincidence of the *index name*
   (`ux_sale_lines_sale_line`) containing that substring — the table is
   aliased `sl`, and the reviewer confirmed SQLite prints the bare alias
   (`SCAN sl`) once a different access path is chosen, which would silently
   false-fail (or worse, fail to detect a real second scan, since it can
   only ever match once regardless of scan count). Changed to check the
   plan detail's second whitespace-separated token equals the alias `sl`
   exactly.
3. **Fixed in this round — missing coverage: lunar k=3 was never
   exercised**, and no test combined a k=1 solar/lunar overlap with
   independent k=2/k=3 signal in the same query (the shape most likely to
   expose an args/window-index mixup in the collapsed query). Added
   `TestPOSRepo_SeasonalForecast_ThreeYearsWithOverlapAndLunarK3` using the
   reviewer's own manually-verified scenario (Expected 8.0, Years 3, not
   Lunar).
4. **Fixed in this round — stale comment** referencing the now-deleted
   `seasonalWindowSQL` constant in the test file's doc comment; reworded.
5. **Not applicable after this round**: the reviewer's note on the
   `seasonalWindowQuery(nil, ...)` producing invalid SQL — added
   `if len(windows) == 0 { return "", nil }` as a self-evident contract on
   the now-independently-callable/-tested builder function, even though
   `SeasonalForecast`'s own `yearsAvail == 0` early-return already makes it
   unreachable from the real caller.

No blocker-class issue (money/tax, data loss, security) — no second review
round; all fixable findings were mechanical (one-line guard, one-line empty
check, a test-assertion tightening, a comment) and applied directly.

## Verification performed (this session, after applying the review's fixes)

- `go build ./...`, `go vet ./...`, `gofmt -l` — clean.
- `go test ./internal/data/... -run Seasonal -v` — 13/13 pass (11
  pre-existing + 2 new query-builder tests + the 2 new regression tests
  added in this round).
- Re-verified the NaN-guard fix's TDD claim personally: reverted just the
  `if firstK == 0 { continue }` line, reran the new test, confirmed it
  fails with `Expected:NaN Years:0` exactly as predicted, restored, reran
  clean.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh` — both green.
- `go test ./... -race` — every package passes except the pre-existing,
  unrelated `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`), which fails under this sandbox's root-run
  environment (root defeats a read-only-directory check) — tracked
  separately as ut-docs#415; confirmed it has no dependency path to
  `internal/data` and fails identically on `origin/main` before this diff.

## Scope

`internal/data/pos_repo.go` (repository layer only) and
`internal/data/pos_repo_seasonal_forecast_test.go`. No migration, no new
page route, no i18n/UI/plugin/offline-first surface — pure backend query
restructuring with byte-identical output (verified by the 8 unmodified
pre-existing exact-value tests, plus 2 new ones covering the gap the review
found).

## Outcome

Independent review found the SQL rewrite itself correct (arg ordering,
old-vs-new semantic equivalence, no injection risk) and the acceptance
criterion met (`EXPLAIN QUERY PLAN` shows `sale_lines` scanned exactly
once, down from up to 6). One real defensive-correctness gap found and
fixed (NaN-poisoning guard, currently unreachable but restored to match old
behaviour), plus a fragile test assertion tightened and a real coverage gap
(lunar k=3) closed. Safe to merge.
