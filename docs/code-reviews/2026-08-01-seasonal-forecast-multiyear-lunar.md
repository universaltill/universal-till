# Code review — SeasonalForecast: multi-year averaging, lunar windows, category rollups (ut-docs#84)

- **Date:** 2026-08-01
- **Branch:** `feature/ut84-seasonal-forecast`
- **Scope:** `internal/data/pos_repo.go` (SeasonalUpcoming → SeasonalForecast),
  `internal/pages/reports_page.go`, `web/ui/pages/reports.html`,
  `web/locales/{en,fa,ar,tr}.json`, tests in `internal/data` + `internal/pages`.
- **Reviewer:** independent different-model subagent (Opus), briefed on the
  exact diff, repo rules, and told to run the gate itself and be adversarial.

## What shipped

The "Coming up" order-ahead report card previously projected only the same
upcoming 28-day window exactly one year back. Now:

1. **Multi-year averaging** — up to 3 prior years; per item the divisor is
   the span back to its oldest signal, so a newcomer isn't diluted by shop
   history that predates it and a faded line is averaged down.
2. **Lunar-window shift awareness** — each prior year is inspected through a
   solar window (k×365d back) AND a lunar window (round(k×354.37)d back);
   the year contributes `max(solar, lunar)`. Moving-holiday demand (Ramadan
   shifts ~11 days/year) is caught with no hardcoded holiday list. Items
   whose lunar signal dominates get a 🌙 badge.
3. **Category rollups** — the same forecast summed per category;
   `SuggestQty` is the SUM of member items' suggestions (one item's surplus
   can't cover another). Hidden when every item is uncategorized.

## Independent review findings and disposition

| # | Severity | Finding | Disposition |
|---|----------|---------|-------------|
| 1 | blocker | `rows.Err()` never checked after scan loops (`rows.Close()` provably can't surface iteration errors — dead check); context cancellation mid-scan would ship a silently partial forecast | **Fixed** — both scans restructured into closures with `defer rows.Close()` + `return rows.Err()` |
| 2 | should-fix | Category `Expected`/`OnHand` accumulate rounded floats and render raw drift (`3.3000000000000003`) into the HTML | **Fixed** — re-rounded to 1dp on assembly; item `OnHand` (SUM of REALs) rounded too |
| 3 | should-fix | Category-rollup test was a tautology — fixture numbers made SUM-of-suggestions and ceil(exp−onhand) agree; proven by mutation (wrong implementation passed the whole suite) | **Fixed** — bread now has a genuine surplus (20 on hand vs 10 expected): SUM=6 vs forbidden ceil=0. Mutation re-applied post-fix: test fails as intended |
| 4 | should-fix | Variant-tracked items reported `OnHand: 0` (inventory rows for variants have `item_id NULL` by schema CHECK) → phantom reorder suggestions; pre-existing in SeasonalUpcoming, amplified by the rollup | **Fixed** — on-hand query folds variant stock to the parent item via `LEFT JOIN item_variants`; new regression test (50 units variant stock → OnHand 50, suggest 0) |
| 5 | should-fix | Rollup covers ALL items while the item table is top-10 — the two tables can visibly disagree | **Fixed (clarified)** — full rollup is the more useful number; label now reads "By category (all seasonal items)" in all four locales; doc comment states the contract |
| 6 | should-fix | `max(solar, lunar)` over ~61%-overlapping k=1 windows upward-biases lumpy demand even for shops with no lunar exposure | **Accepted as deliberate** — missing a real moving-holiday spike costs a shop more than a modestly generous advisory hint; now stated explicitly in the doc comment. Revisit if field feedback shows over-ordering |
| 7 | info | 6 full sales-table scans per render (`datetime()` wrapper defeats `idx_sales_created`; wrapper is required by the RFC3339-vs-space storage trap) | **Follow-up card** ut-docs#199 (structural: normalized indexed column or single CASE-bucketed pass) |
| 8 | nit | `days > 180` silently became 28 | **Fixed** — clamps to 180 |
| 9 | nit | `limit <= 0` semantics changed (SQL LIMIT 0 = none → now unlimited) | **Fixed (documented)** in the method comment |
| 10 | nit | `Years` doc said "years contributing" but holds the span incl. gap years | **Fixed** — comment corrected |
| 11 | nit | no `defer rows.Close()` (panic-path leak) | **Fixed** with #1 |
| 12 | nit | sort tie-break nondeterministic for duplicate display names | **Fixed** — final tie-break on item id |
| 13 | nit | `lunarYearDays` pinned by no test (355.0 passed the suite) | **Fixed** — boundary test at −326.5d; mutation to 355.0 now fails it |
| 14 | info | faded items keep a shrinking suggestion until they age past the 3-year horizon | **Accepted as intended**, documented in the type comment |

Reviewer confirmed clean: SQL injection surface (bound int-derived params only),
money typing (quantities only, no `money.Money` floated), window boundary
arithmetic vs the old geometry, NULL handling, i18n (all 4 locales key-complete,
translations genuine, `reports.seasonal_lastyear` fully removed), HTML escaping
of the badge title attribute, RTL/logical CSS, offline-first (pure local
SQLite), no file writes / cwd-relative paths, no real shop names or secrets.

## Verified beyond automated tests

- **TDD claims re-verified personally**: new repo tests were written first and
  watched fail (compile-level red); the reviewer's two mutations (#3, #13) were
  re-applied after the fixes and now fail exactly as intended, then restored
  with the full suite green (`-count=1`).
- **Real driven run**: throwaway till booted from the built binary, seeded
  2 years of history + a lunar-only burst; live `/reports` showed
  Expected 15 = (20+10)/2, the 🌙 badge on the shifted item, and the Drinks
  rollup 24/78 — numbers hand-checked. Server killed, data dir removed.
- **Playwright** `pages.spec.ts` green (real browser smoke incl. `/reports`).
- Full gate: `go build`, `go vet`, `go test ./... -count=1`, both CI guards.

## Verdict

Safe to merge. Deferred items: ut-docs#199 (perf), #6's bias choice flagged
for field feedback via the close-out note on ut-docs#84.
