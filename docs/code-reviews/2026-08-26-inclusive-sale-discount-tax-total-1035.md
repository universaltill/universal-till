# Code review: inclusive-priced sale discount tax_total (ut-docs#1035)

**Date:** 2026-08-26
**Card:** ut-docs#1035
**Complexity:** medium — build: inline (Sonnet), review: Opus (fresh-context subagent, shared checkout)

## What shipped

`computeSaleTotals` (`internal/pos/sales.go`) persisted `sales.tax_total`
as a flat per-line sum, never adjusted for a whole-sale discount
(`SaleInput.SaleDiscount`) — while `VATBandsForSale` (`vat_breakdown.go`,
the shared function `invoice_page.go`/`eod_tax_bands.go` already use to
reconstruct a sale's VAT bands) correctly apportions the discount and
re-derives `Tax` per rate band for inclusive-priced sales. The two
disagreed for exactly one shape: an inclusive-priced sale carrying a
whole-sale discount. Reproduction from the ticket: €11.90 inclusive @19%
with a €1.90 discount persisted `tax_total=190` (undiscounted) while
`VATBandsForSale` — and therefore the invoice's VAT table and the
day-close bands — already showed 160.

Fix: `computeSaleTotals` now builds a `[]VATLine` per line as it iterates
(capturing `ComputeTaxBasisPoints`'s previously-discarded second return,
the line's gross amount) and derives `taxTotal` from
`VATBandsForSale(vatLines, in.SaleDiscount.Minor(), in.TaxInclusive, 0, 0)`
instead of the old flat sum. `serviceCharge=0` keeps this scoped to
exactly the line+discount figure, orthogonal to the existing `chargeTax`
step (unchanged, still added after) — no double-counting.

## Independent review (Opus, fresh-context subagent, shared checkout)

Read the diff cold, re-derived the math by hand, and ran a 20,000-case
randomized fuzz (1–4 lines, rates {0,7,10,19,20,21}%, both pricing modes,
line discounts, whole-sale discounts, service charges, flat
`ServiceChargeTaxBasisBP` answers) proving `computeSaleTotals`'s
`taxTotal` equals `sum(VATBandsForSale(...).Tax)` **exactly** — zero
divergences, and confirming `serviceCharge=0` never double-counts.
Independently re-verified the TDD claim (reverted `sales.go` against the
pre-review commit, ran the new tests, confirmed a real red with accurate
messages, restored). Ran the full gate independently (gofmt, build, vet,
`-race`, full `go test ./...`, all 29 `ci.yml` build-job guards) — all
green. Confirmed: vouchers unaffected (added after the discount/tax path,
never in `vatLines`), no signed fiscal record changes (`vat_breakdown` on
the wire is documented pre-discount by contract and carries no
`tax_total`), sync replay across mixed pre/post-fix versions doesn't trip
the payment-sufficiency check (inclusive `total` never depended on
`taxTotal`), refunds unaffected (`computeRefundTotal` uses only `total`).
Agreed no ADR is needed — this restores an invariant
`eod_tax_bands.go`'s own doc comment already states as binding
(`sum(band.Tax) == TaxNet`), it doesn't introduce one.

## Findings — fixed before merge

**F1 (should-fix) — the live basket panel disagreed with the receipt.**
`Service.recomputeTotals` (`service.go`, the pre-tender on-screen basket a
cashier actually looks at) had the identical flat-sum bug, independently
of `computeSaleTotals` — so post-fix, a cashier applying a whole-sale
discount to an inclusive sale would see `Tax: €1.90` on screen and `€1.60`
on the printed receipt/invoice. `Total` was correct on both sides (nothing
was mischarged), but the two VAT figures disagreed. Fixed the same way:
`recomputeTotals` now builds `[]VATLine` per line and derives `tax` via
`VATBandsForSale`, `serviceCharge=0` for the same orthogonality reason.
New test: `TestBasketTax_InclusiveDiscountMatchesPersistedSaleTax`
(`gross_inclusive_invariant_test.go`) — the same repro, asserting the
basket panel now reads 1.60.

**F2 (should-fix) — the fix's own load-bearing claim had no test.**
`eod_tax_bands_test.go`'s existing whole-sale-discount test
(`TestEODTaxBands_WholeSaleDiscountThroughCompleteSale`) is
exclusive-only; the inclusive mirror — the exact shape #1035 broke — did
not exist, so `assertEODTaxBandIdentities`'s
`sum(band.Tax) == rep.TaxNet` invariant (documented there as "a Z-report
whose printed rows don't add to its printed totals is legally unusable")
was never actually exercised for this bug. Added
`TestEODTaxBands_WholeSaleDiscountInclusiveThroughCompleteSale`, the same
repro through the real `CompleteSale` → `EndOfDay` path; confirmed it
would have failed pre-fix (`TaxNet=190` vs. `bands.Tax=160`) and passes
now.

**F4 (should-fix) — over-discount could persist a negative tax_total.**
Nothing caps `SaleDiscount` above the sale's subtotal. Pre-fix, an
over-discounted inclusive sale already produced a meaningless (but
positive) `tax_total`; post-fix, `VATBandsForSale`'s band `Gross` goes
negative in that case and integer division yields a *negative* `Tax`
(confirmed by hand: gross 100 @19%, discount 500 → `taxTotal=-64`). Not a
regression in the sense of "previously correct, now wrong" — both are
already-meaningless over-discount output — but a negative tax figure is a
worse shape to persist or print than a positive-but-wrong one, and
`total` already floors at 0 for the identical case one line below. Added
the same floor to `taxTotal` in both `computeSaleTotals` and
`recomputeTotals`. New tests:
`TestComputeSaleTotals_OverDiscountClampsTaxTotalNonNegative` and the
assertion is implicitly covered on the basket side by the same floor
logic (not a new UI-reachable state — `SetDiscount`/`SetDiscountPercent`
already don't reject an over-large discount, which is a pre-existing,
separate UX question, not this ticket's scope).

**F7 (nit) — the multi-rate parity test's independent reconstruction was
too easy to satisfy.** It built its own `[]VATLine` from `l.UnitPrice`
directly, bypassing `Qty`/`LineDiscount` — equivalent only because the
fixture happened to use `Qty=1` with no line discount, so a qty or
line-discount mistake in production's own line-building loop wouldn't
have been caught. Reworked the fixture to `Qty=2` + a line discount and
the reconstruction to go through `AmountForQuantity(...).Sub(LineDiscount)`
the same way `computeSaleTotals` does; re-derived the expected figure by
hand (139, down from the original fixture's 155) and confirmed the test
still passes.

**F6 (nit) — undocumented rounding-rule difference.** `VATBandsForSale`'s
discount re-derivation for inclusive pricing (`Net = Gross*10000/(10000+
rate)`) truncates, while `ComputeTaxBasisPoints` (the undiscounted
per-line path) rounds half-up — so a discounted inclusive band's tax can
differ by up to 1 minor unit from what the same figures would give
undiscounted. Confirmed by the reviewer this is consistent across every
caller already and biased toward declaring slightly *more* tax, never
less (the safe direction), so not a defect — added a comment at the
truncation site so the next reader doesn't assume both paths round
identically.

## Explicitly deferred / follow-up (not built here)

**F3 — historical (already-persisted) sales stay inconsistent, on
purpose, for a reason stronger than "not urgent."** The ticket's own text
justified skipping a data migration as "not urgent — no evidence this
reached a filed VAT return." The independent review verified the sharper
reason directly: a pre-fix sale row re-read by this post-fix build still
produces `bands.Tax=160 != TaxNet=190` for that historical row — the
day-close Z-report for any *historical* day containing this exact sale
shape is internally inconsistent, silently, forever, unless migrated. Not
a regression (bands always came from `VATBandsForSale`; only
`sales.tax_total` changes going forward), so not a merge blocker. The
stronger reason **not** to silently `UPDATE sales.tax_total` on an
already-completed sale: for a German shop with a TSE-signed sale, the
`sales` row and its signed record are meant to be immutable together
(GoBD) — a background reconciliation script rewriting a persisted tax
figure after the fact is itself in tension with that, arguably worse than
leaving a known, narrow, documented historical gap. Filed as a follow-up
Backlog card (a real decision — reconcile going forward only vs. a
signed, audited correction pass vs. leave as documented known-gap — for
whoever scopes it, not silently decided here): **ut-docs#1114**.

**F5 — `POSRepo.TaxSummary` (Reports → Tax tab) widens further from
`VATBandsForSale`.** Pre-existing raw-SQL aggregation over
`sale_lines.tax_amount`/`total_before_tax`
(`internal/data/pos_repo.go:1073-1098`) — exactly the SQL-only approach
ut-docs#1003 replaced for the day-close Z-report because it can't see
sale-level (whole-sale discount) amounts. It already disagreed with the
day-close bands before this fix; this fix widens the same pre-existing
gap for the Tax tab specifically. Out of scope for this ticket (a
repository-layer change, not a `computeSaleTotals` consistency fix) —
filed as follow-up: **ut-docs#1115**.

**F8 — vestigial dead code carries the original bug.** `corepos/sales.go`
is an unimported copy of `internal/pos` (confirmed: `grep` for
`universal-till/corepos` outside `corepos/` itself returns nothing) with
the identical pre-fix `computeSaleTotals`. Harmless today, a landmine if
ever revived. Filed as a cleanup follow-up: **ut-docs#1116**.

## What was verified beyond automated tests

- Hand-derived the repro's numbers independently of the code (both
  reviewer and Dev did this separately and agreed): 190→160 for the
  single-rate case, 193→155 (later 139 after F7's fixture rework) for the
  multi-rate case.
- Confirmed `ServiceChargeTax` (called in `computeSaleTotals`) and
  `VATBandsForSale`'s own service-charge apportionment both bottom out in
  the same `ApportionServiceChargeTax` call — so the untested
  inclusive+service-charge+discount combination is provably consistent by
  shared-function construction, not merely assumed.
- No UI/template/JS surface touched beyond the basket panel's numeric
  output (no new markup, no new i18n key) — no browser drive needed;
  `guard-i18n.sh`/`guard-data-access.sh` both re-run clean.

## Verdict

Safe to merge. Two should-fix findings (F1, F2) were the fix's own actual
scope gaps and are now fixed in-branch with regression coverage; two more
(F4 nit-adjacent safety clamp, F7 test rigor) fixed as cheap hardening.
Three findings (F3, F5, F8) are real but out of this ticket's scope and
are tracked as named follow-ups rather than silently dropped. Full gate
green after all fixes: `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./internal/pos/... -race`, `go test ./...` (all packages, zero
failures), `guard-data-access.sh`, `guard-i18n.sh`.
