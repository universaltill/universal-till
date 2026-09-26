# Code review: ADR-0062 step 2/3 — multi-charge totals + tax apportionment (#985)

- **Card:** universaltill/ut-docs#985 (step 2 of 3 from #963; #984 is done,
  #986 follows)
- **Design:** ADR-0062, plus Amendment A (ut-docs#2933: the rounding
  difference is two-sided)
- **Author:** Opus 5.5 dev subagent. **Reviewer:** independent Fable
  subagent in an isolated worktree, with mutation testing.

## What shipped

- `pos.ChargeInput` and `SaleInput.Charges []ChargeInput` replace the scalar
  `ServiceCharge` / `ServiceChargeTaxBasisBP` (ADR Decision 1).
  `SaleInput.ChargesTotal()` / `pos.SumCharges` give the display sum.
- `pos.BuildCharges` (new `internal/pos/charges.go`) is the one list builder
  shared by the basket preview (`Service.computeTotals`) and the tender
  handler (`pos_api.go`). It builds the merchant-rate `service_charge` item
  plus every plugin-declared item, applied verbatim.
  - The Turkey ban suppresses the whole list. The basket learns about it
    from a neutral `Config.ChargesForbidden`, set from the existing
    `common.ServiceChargeForbidden` at all 6 `pos.Config` sites; no new
    country list.
  - `permitted=false` drops only the merchant item.
  - Non-positive amounts are dropped.
- `ApportionChargesTax` / `ChargesTax` loop the unchanged
  `ApportionServiceChargeTax` per charge and aggregate bands by rate.
  `computeSaleTotals` rejects any negative charge, item by item.
- `CompleteSale` writes `sale_charges` rows in the same transaction.
  - `sales.service_charge_amount` = the sum.
  - `service_charge_tax_basis_bp` = the charge's basis when there is exactly
    one charge, else 0.
- Compile-level adaptations, identical for 0 or 1 charge:
  - Sync replay and refunds build a one-item `service_charge` list.
  - fiscal-sign `vat_breakdown` folds via `ApportionChargesTax`, and its flat
    field is the sum.
  - The fiscal-device hook uses the sum.
- README service-charge bullet updated. `web/help/img/manifest.json` has only
  a `surface_sha256` refresh (no PNG changed; commit carries
  `Docs-Shots-Unchanged: true`).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| D1 | design | ADR-0062 claimed per-charge tax is ≥ sum-then-apportion by ≤ N−1. False: per-charge half-up rounding goes both ways (3+3 over 10.00@0%/10.00@20% → 0 vs 1). | ADR Amendment A (ut-docs#2933). Tests pin both directions plus the proven bound over 20k random inputs, instead of the false `0 ≤ Δ ≤ N−1`. |
| R1 | should-fix | Self-order kiosk and table-QR preview (`KioskEngine`) now also shows plugin levies, but kiosk checkout (`kioskSaleLinesAndTotal`) never demands any charge. This already happens on `main` for the merchant service charge; this change widens it to levies. | Follow-up card filed. Latent: no plugin declares levies. A proper fix touches the settings push and table-QR config copies, which is out of scope here. |
| R2 | should-fix | For any 2+-charge sale, EOD tax bands (`internal/pos/eod_tax_bands.go`, `internal/pages/eod_method_tax_bands.go`) and the invoice re-apportion the sum at basis 0, so the Z-report bands ≠ `sales.tax_total`. | Added to #986's scope (which already gates any levy plugin). Latent today. |
| R3 | nit | Not byte-identical for a zero-amount charge: it no longer carries the policy's basis. | Doc comment on `derivedChargeTaxBasisBP` softened. Harmless. |
| R4 | nit | Over-discount: `main` rejected the tender with a negative service charge; now the charge is dropped and the sale completes at total 0. | Accepted as an improvement. Pinned by `TestBuildCharges_ZeroAndNonPositiveAmountsDropped` (negative base → no charges). |
| R5 | nit | Commit needs the `Docs-Shots-Unchanged: true` trailer | Done |
| R6 | nit | `ChargesTotal` value receiver copies a large struct | Changed to a pointer receiver |
| R7 | nit | Sync replay of a flat-basis levy at basis 0 can drift by more than rounding, and a replay whose re-derived total lands higher is rejected as underpaid. | Already #986's scope (journal rebuild from `charges`). Noted there: #986 must land before any levy plugin. |

## Verified beyond automated tests

The reviewer mutation-tested the diff. Each mutation was caught by its
intended test:
- Tender ignores the policy → parity test (3338 vs 3100).
- Preview or tender ignores the ban → Turkey test.
- `InsertSaleCharges` skipped → persistence test.
- Basis taken from the first of 2 charges → persistence subtest.
- Negative check removed.
- Band sort removed.
- Fiscal fold uses only the first charge.
- `BuildCharges` skips plugin items.
- `ChargesTax` computed as one pass.

The reviewer also:
- Hand-recomputed the parity test's exclusive figures (net 2332; charges
  292/117/93; charge tax 81; total 3338) and the Omani example.
- Grepped every `SaleInput{` and `pos.Config{` site.

The orchestrator:
- Ran the Playwright checkout specs: 20/20 across split-tender, compact
  tender, payment overlay, receipt reset, voucher split and deposit refund.
- Ran the full gate on the final diff: `gofmt`, build, vet, `go test ./...`,
  golangci-lint (0 issues), and every `ci.yml` build-job guard.
  `guard-deadcode-baseline.sh` flags `internal/logging` locally only,
  because this container lacks GTK headers to compile `cmd/unitill-desktop`
  (its caller). It also fails without this diff, and the diff doesn't
  touch logging.

No visual surface changed: 0- and 1-charge totals are identical, so there
are no screenshots.

## Deferred

- #986 (receipt/journal/invoice itemization, fiscal `charges` array and
  contract 1.6.0, journal rebuild, settings list), now also covering the
  EOD readers.
- New card: kiosk/table-QR charge parity.
- Multi-charge refund proration.
- `WorkerAllocationsSummary`'s `service_charge` source.

## Verdict

**Safe to merge** for step-2 scope. No blockers; should-fix items are latent
until a levy-declaring plugin exists, which #986 gates.
