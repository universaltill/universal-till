# Code review: day-close per-VAT-rate totals (ut-docs#1003)

## Change

`data.EODReport` gains `TaxBands []TaxBand` — per-VAT-rate net/tax/gross
for the German day-close (Z-Bon), required for a legally usable VAT
return / DATEV export. Printed as a new "BY VAT RATE" footer section on
the Z-report receipt, same precedent as the existing Departments/Tills
footers.

Two commits: the first implementation (`1414dd9d`) computed bands with a
single SQL aggregate over `sale_lines`, grouped by `tax_rate_bp`. An
independent review (Opus, fresh context, no prior reasoning about the
change) found this silently missed two sale-level amounts with no
`sale_lines` row — the service charge's own VAT (ADR-0061 — always taxed)
and a whole-sale discount (`sale_discounts`, never distributed into
lines) — so any sale carrying either broke the report's own required
identities. The second commit (`e553b888`) fixes this by routing through
the same shared per-sale VAT-banding math the invoice's VAT table already
used, extracted into `internal/pos` so both callers share one
implementation.

## Review process (this card, in order)

1. **First independent review — Opus, fresh context.** Found the
   service-charge/discount gap above, empirically reproduced with real
   `pos.CompleteSale` calls (not hand-inserted fixtures), with exact
   before/after figures. Also found: `TaxSummary` never set the new
   `Gross` field (dead-field bug), `fmtRateBP` mishandled a negative
   basis-point input, and the VAT footer's fixed-width columns clip
   above ~£999,999.99. Full findings on ut-docs#1003.
2. **Fix** (Fable, fresh context, given the reviewer's exact findings +
   file:line references): extracted `internal/pages/invoice_page.go`'s
   `vatBreakdown` per-sale math (line banding, gross-share discount
   proration with largest-remainder rounding, `ApportionServiceChargeTax`
   apportionment) into `internal/pos/vat_breakdown.go` as
   `VATBandsForSale`/`InferTaxInclusive`, verbatim — not re-derived —
   specifically so the invoice VAT table and the day-close bands can
   never disagree (ADR-0061's own requirement for the service-charge
   apportionment specifically, extended here to the whole computation).
   Moved the day-close band computation out of `internal/data` (which
   cannot import `internal/pos` — `internal/pos` already imports
   `internal/data`, so the reverse would cycle) into
   `internal/pages/eod_tax_bands.go`, fed by a new repo method
   `POSRepo.SalesForTaxBands` (two fixed queries regardless of sale
   count, no N+1).
3. **This review pass (Sonnet, orchestrating session)** — scoped to the
   fix, not a full re-review of the whole diff (per this pipeline's own
   process-depth rules: a second full round has to be earned by a
   blocker-class finding, and is scoped to the fix once earned):
   - Read `internal/pos/vat_breakdown.go` in full against the original
     `vatBreakdown` it was extracted from — confirmed the math is a
     faithful port (line banding, largest-remainder discount proration,
     inclusive/exclusive handling, `ApportionServiceChargeTax` call) with
     no behavior drift.
   - Read `internal/pages/eod_tax_bands.go` and `POSRepo.SalesForTaxBands`
     — confirmed the local-calendar-day window matches `dateRangeSummary`
     exactly (`date(created_at,'localtime') BETWEEN date(?) AND date(?)`,
     ut-docs#869), zero-value note-line exclusion is preserved at the
     query, voided sales are excluded (`status = 'completed'`), and
     returns sign-flip via `SaleType == "return"`.
   - Re-ran the full gate personally: `gofmt -l .` (clean), `go build
     ./...` (clean), `go vet ./...` (clean),
     `go test ./internal/data/... ./internal/pages/... ./internal/pos/...`
     — all packages `ok` (data 60.5s, pages 74.7s, pos 1.5s, catalog,
     common).
   - Ran the two new regression tests explicitly:
     `TestEODTaxBands_ServiceChargeSaleThroughCompleteSale` and
     `TestEODTaxBands_WholeSaleDiscountThroughCompleteSale` — both real
     `pos.CompleteSale` calls, both PASS, confirming the exact
     reproduction cases the first review found are now fixed.
   - Confirmed `internal/pages/invoice_page.go`'s existing invoice/
     credit-note/refund test suite (`Invoice|VAT|Refund` filter, ~25
     tests) passes unmodified — `vatBreakdown` is now a thin adapter over
     the shared function with byte-identical output.
   - `scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` —
     both pass (new SQL is confined to `internal/data`; no new
     user-facing template strings — `print.Doc` footer text follows the
     existing untranslated-footer precedent, same as Departments/Tills).
   - `-race`: confirmed the golden new-test package (`internal/data`,
     `internal/pages`) passes under `-race` for the specific new tests
     in isolation. A full-package `-race` run of `internal/pages` hits
     Go's default 10-minute test timeout — verified this is **pre-existing
     on clean `origin/main`, unrelated to this change** (reproduced in an
     isolated worktree with zero modifications; the goroutine dump names
     an unrelated pre-existing test). CI itself never runs `-race` on the
     full suite for exactly this class of issue (see `ci.yml`'s own
     comment on `internal/plugins`'s wider timeout). Filed as ut-docs#1034
     — not a blocker for this card.

## Known, deliberately out-of-scope gap

An inclusive-priced sale with a whole-sale discount still leaves
`sales.tax_total` (persisted by `computeSaleTotals`, unrelated to this
card) higher than what the shared VAT-banding math correctly re-derives
— a pre-existing discrepancy the invoice's VAT table has had all along
(this card's bands now surface it at the day-total level too). Fixing it
means changing what the engine persists, well beyond this card's scope.
Filed as ut-docs#1035.

## Outcome

Merging as-is. Closes universaltill/ut-docs#1003.
