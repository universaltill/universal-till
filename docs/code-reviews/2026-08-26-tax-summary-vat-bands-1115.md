# Code review: Reports → Tax tab bands via VATBandsForSale (ut-docs#1115)

**Date:** 2026-08-26
**Card:** ut-docs#1115 (follow-up F5 from the 2026-08-26 ut-docs#1035 review)
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context
subagent, isolated worktree of `fix/1115-tax-summary-vat-bands` @ `d192415`,
"WIP: pre-review snapshot")

## What shipped

`POSRepo.TaxSummary` backed the Reports → Tax tab with a raw-SQL
`GROUP BY sl.tax_rate_bp` aggregation straight over `sale_lines` — the
exact shape ut-docs#1003 had already removed from the day-close Z-report,
because it cannot see the two sale-level amounts that have no `sale_lines`
row at all: a whole-sale discount (`sales.discount_total`) and a service
charge (`sales.service_charge_amount`). ut-docs#1035 then widened the gap:
`sales.tax_total` started correctly discounting an inclusive-priced sale's
tax while the Tax tab's independent SQL sum did not, so the Tax tab, the
Reports header KPI and the Z-report could show three different VAT
figures for the same sales.

The change splits fetch from math, matching the Z-report's existing
structure:

- `internal/data/pos_repo.go` — `TaxSummary` replaced by
  `SalesForTaxWindow(ctx, from, to time.Time) ([]EODTaxBandSale, error)`,
  a **raw fetch only** (no netting, no banding), modelled on the existing
  `SalesForTaxBands` but with `windowArgs` half-open `[from, to)`
  windowing instead of the Z-report's calendar-day `BETWEEN`, because the
  Tax tab takes an arbitrary rolling window. Two fixed queries (sales,
  then lines) — no N+1 — with lines attached per sale via an `idx` map.
  Payments are deliberately not fetched (nothing downstream builds a
  `MethodTaxBand` cross-tab).
- `internal/pages/tax_summary.go` (new) — `computeTaxSummary` fetches via
  `SalesForTaxWindow` and hands the result to the **existing, unmodified**
  `computeEODTaxBandsFromSales`, so the Tax tab runs the same per-sale
  `pos.VATBandsForSale` call, the same return sign-flip, the same
  per-rate merge and the same ascending sort the Z-report already does.
  It lives in `internal/pages` for the reason `eod_tax_bands.go` already
  documents: `internal/data` cannot import `internal/pos`.
- `internal/pages/reports_page.go:313` — `case "tax":` now calls
  `computeTaxSummary(...)` instead of `repo.TaxSummary(...)`. Template,
  row struct and data shape into `web/ui/partials/reports_tab_tax.html`
  are unchanged.
- Tests — `TestPOSRepo_TaxSummary_BandsReturnsSubtract` rewritten as
  `TestPOSRepo_SalesForTaxWindow_FiltersAndAttachesLines` (fetch contract
  only: completed-only, in-window-only, lines attached, a return comes
  back *unsigned*); `product_reports_test.go` callsite updated; two new
  acceptance tests in `tax_summary_test.go`:
  `TestTaxSummary_AgreesWithEODTaxBands_WholeSaleDiscountInclusive` (the
  card's own stated criterion) and
  `TestTaxSummary_AgreesWithEODTaxBands_ServiceChargeAndReturn` (added
  after review, see F5 below).
- `web/help/img/manifest.json`/`web/help/img/tr/invoices.png` —
  screenshot surface hash regenerated (`make docs-shots`); the tab's own
  screenshot is unaffected (no route/topic covers `?tab=tax`), the
  `invoices.png` delta is render noise (7 bytes).

## Independent review (Opus, fresh-context subagent, isolated worktree)

Read the diff cold against `main`, re-derived the money math by hand,
independently re-verified the TDD claim by replaying the deleted code,
and ran the full gate.

**Architecture/correctness confirmed:** `computeEODTaxBandsFromSales` is
window-shape-agnostic (one independent `pos.VATBandsForSale` call per
sale, merged into a `map[int]*data.TaxBand` — nothing in it reads a day
or window), so reusing it for a rolling window is correct.
`SalesForTaxWindow`'s SQL is a faithful re-window of `SalesForTaxBands`:
same `status = 'completed'` filter, same zero-value-line exclusion, same
`ORDER BY sl.sale_id, sl.line_no` (load-bearing — `VATBandsForSale`'s
remainder allocation depends on line order), same scan order and error
wrapping. Two silent *improvements* over the deleted code: `COALESCE`
on a possibly-NULL `tax_rate_bp` (old code would have errored on scan),
and the zero-value "note" line exclusion (old aggregate could invent a
spurious band). No `internal/data` → `internal/pos` import introduced;
`guard-data-access.sh` green; `grep` confirmed no remaining caller of the
deleted `TaxSummary` anywhere.

**Hand-derived math for the acceptance fixture** (€11.90 @19% inclusive,
€1.90 whole-sale discount): gross after discount 1190−190=1000; inclusive
VAT 1000×19/119=159.66…→160; net 840. Matches `{RateBP:1900, Net:840,
Tax:160, Gross:1000}` — both the new test's want and the Z-report's own
independently-computed band.

**TDD re-verified personally, not taken on trust:** replayed the deleted
`TaxSummary` body verbatim (`git show 35d1060:internal/data/pos_repo.go`)
against the same fixture the acceptance test builds via real
`pos.CompleteSale`:

    OLD TaxSummary bands : [{RateBP:1900 Net:1000 Tax:190 Gross:1190}]
    NEW computeTaxSummary: [{RateBP:1900 Net:840  Tax:160 Gross:1000}]
    Z-report TaxBands    : [{RateBP:1900 Net:840  Tax:160 Gross:1000}] (TaxNet=160 Net=1000)

The old code genuinely produced 190 and disagreed with the Z-report; both
assertions in the new acceptance test would have fired pre-fix — a real
red-before-green, not a tautology, and through the real `CompleteSale`
write path (the original SQL-only Z-report bug once passed hand-inserted
fixtures while dropping both sale-level amounts on every real sale — the
same trap this test avoids).

**Manual check:** `web/help/en/reports.md` mentions the Tax tab only as a
tab name and "tax summary" — no figures, no row order, nothing
describing the old wrong numbers. No prose update needed, confirmed by
reading the file rather than assumed.

## Findings, and what happened to each

| # | Severity | Location | Finding | Outcome |
|---|---|---|---|---|
| F1 | should-fix | `internal/data/export_repo.go:218`, `internal/data/pos_repo.go:875` | Two comments still named the deleted `TaxSummary` method. | **Fixed** — both now point at `SalesForTaxWindow`/`computeTaxSummary` (ut-docs#1115). |
| F2 | nit | `internal/pages/reports_page.go:313` (via `eod_tax_bands.go:76`) | Tax tab row order flips from `RateBP DESC` (old SQL) to ascending (new path, matching the Z-report's own 0/7/19% layout). Verified harmless: bare `{{ range }}` template, no test/e2e/docs-shot asserts order, manual doesn't describe it. | **Accepted, noted here** — arguably an improvement; called out in this record and the PR body rather than reverted. |
| F3 | nit | `internal/pages/tax_summary.go`, `internal/data/pos_repo.go:1085` | Doc comments overclaimed the Tax tab "can never disagree with the Z-report" — true for the banding math, not for the window: `SalesForTaxWindow` doesn't apply the business-day shift (`reports.business_day_start`, ut-docs#519) that `EndOfDay`/`EndOfDayRange` do, so a shop with a late boundary can see a `?period=day` Tax tab and that day's Z-report cover genuinely different sales. Pre-existing (the old `TaxSummary` had the same gap), not a defect in this diff. | **Fixed** — both comments softened to "over the SAME set of sales" with the caveat named explicitly. |
| F4 | nit, deferred | `internal/data/pos_repo.go` (`SalesForTaxWindow`) | Aggregate query → full sale+line materialization for the window (bounded — `?days=` caps at 365 — but a real new cost on Pi-class hardware for a wide window). Same tradeoff `EndOfDayRange`'s `attachEODTaxBands` already accepts. | **Deferred, tracked** — see ut-docs#1145 below; correctness wins over this cost for now. |
| F5 | nit | `internal/pages/tax_summary_test.go` | Only the discount shape was asserted through `computeTaxSummary`; the service-charge shape and returns-netting *composition* (as opposed to each half separately) weren't proven through the new entry point. | **Fixed** — added `TestTaxSummary_AgreesWithEODTaxBands_ServiceChargeAndReturn` (a real service-charge sale + a real return, through `pos.CompleteSale`, compared against `EndOfDayRange`'s own bands over the same window). |

Neither of this pipeline's two recurring bug classes applies (no
filesystem code in the diff: no `os.MkdirAll` gap, no cwd-relative path
where `paths.Data(...)` belongs). No real client/shop name used as
fixture data; no secret-shaped literal.

F4 is tracked as a Backlog follow-up (ut-docs#1145) rather than fixed
here — it's a real, bounded cost, not a defect, and a streaming/chunked
rewrite is its own scoped piece of work, not a one-line fix.

## Verified beyond automated tests

- Hand-derived VAT math for both acceptance fixtures (discount-only, and
  service-charge+return combined) — see above.
- TDD re-verification: the deleted code's exact behaviour replayed and
  confirmed wrong (see above) — not inferred, executed.
- Manual (`web/help/en/reports.md`) read in full — no stale prose.
- Full gate run twice — once before the F1/F3/F5 fixes (by the
  independent reviewer, in its isolated worktree), once after (by this
  session, in the main checkout) — both green, see below.

## Gate (post-fix, main checkout)

- `gofmt -l .` — no output
- `go vet ./...` — clean
- `go test ./...` — full untagged suite, exit 0, all packages pass
- `go test ./internal/pages/... -run "TestTaxSummary_AgreesWithEODTaxBands"` —
  both acceptance tests pass
- `bash scripts/ci/guard-data-access.sh` — green
- `bash scripts/ci/guard-docs-shots.sh` — green (`make docs-shots` run
  after the F3 comment edits touched `internal/pages/tax_summary.go`
  again, since the guard hashes that file's full content; surface
  `0efd6c7ed12a…`)

`-race` was not run on `internal/data`/`internal/pages`: both are
independently known (ut-docs#1119) to hit `go test`'s default 10-minute
`-race` timeout on this sandbox inside unrelated tests' DB-migration
setup — confirmed by the independent reviewer to reproduce identically
on code this diff doesn't touch, so re-running it here would only
reproduce an already-filed environment limitation.

## Verdict

**Safe to merge.** The fix deletes a duplicate, structurally-blind VAT
computation and routes the Tax tab through the single shared
`pos.VATBandsForSale` path the Z-report and the invoice VAT table already
use, so the class of bug ut-docs#1003 / #1035 / #1115 kept re-finding
can't recur on this surface. The repository-pattern boundary is
respected, the shared aggregator is reused unmodified rather than forked,
and both acceptance tests prove real red-before-green against the real
write path. F1, F3 and F5 are fixed in this same branch; F2 is a
harmless, arguably-improving behavior change called out rather than
hidden; F4 is tracked as ut-docs#1145 for a future pass if the Tax tab is
ever reported slow on real hardware.
