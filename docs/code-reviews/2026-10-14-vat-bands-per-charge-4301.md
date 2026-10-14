# Review: VAT bands apportion each charge on its own (ut-docs#4301)

Date: 2026-10-14 · Lane: `lane:cloud-41` · Built by Opus 5.5, reviewed by Fable (different model).

## What shipped

- **universal-till** (`fix/4301-vat-bands-per-charge`):
  - `pos.VATBandsForSale` takes the sale's itemized `[]ChargeInput` and folds them in through `pos.ApportionChargesTax`. Before, it took the summed `sales.service_charge_amount` and one `service_charge_tax_basis_bp` (0 for 2+ charges) and apportioned the sum once at the lines' rates.
  - New `pos.SaleChargesForBanding(rows, serviceCharge, basis)`: the `sale_charges` rows, or one service charge from the scalar columns for a sale recorded without rows. This is the same fallback `refundCharges` uses.
  - `data.EODTaxBandSale.Charges` is filled by one batch query, `attachSaleCharges`, in `SalesForTaxBands` (and `SalesForTaxBandsForTill`), `SalesForTaxBandsInstant` and `SalesForTaxWindow`. That is one extra query per report, not per sale.
  - The invoice `vatBreakdown` reads `SaleDetail.Charges`, which was already loaded.
  - Fixed through those paths: invoice and credit note, receipt VAT table (`print_api.go` → `vatBreakdown`), Z report, method × rate cross-tab, Tax tab, and the cloud sales-aggregate upload.
- **ut-docs** (`fix/4301-vat-bands-per-charge`): `fiscal-sign-ask.md` 1.15.1, a doc-only changelog row. No wire change; signers need to do nothing.

There is no migration: `sale_charges` already stores every charge with its own `tax_basis_bp` (ADR-0062 Decision 2). There is no new ADR: ADR-0062 Decisions 4 and 6 already require every banding path to read the itemized list.

## Verification

- **TDD:** `TestVATBands_MultiChargeBandedPerCharge` (internal/pages) tenders a real sale with three charges (service charge, an apportioned levy, a flat-7 % levy), in both pricing modes. It then checks every reader: invoice, Z report, cross-tab, tax window and instant window.
  - Against the old code every reader failed. For example, exclusive: the 20 % band's tax was 486 instead of 470, and the band tax summed to 514 instead of the sale's `tax_total` of 504.
- **Card repro:** `TestVATBandsForSale_MatchesFiscalPayloadWithFlatLevy` uses the card's own example. On the old code the 10.7 % band was 3/0 against the payload's 56/6.
- The reviewer re-ran both RED → GREEN checks independently in a separate worktree.
- **Reviewer fuzz (throwaway), 20k cases each, seed 4301:**
  - Bands vs `buildFiscalSignPayload`: 1–3 charges with mixed bases, both modes. Every case matched per rate.
  - Σ band tax/gross vs `computeSaleTotals`: with a single-purpose voucher and a discount. Every case matched.
- **Gates:** `go build ./...`, `go vet`, `gofmt`, full `go test ./...`, and the CI `build` job guards. Two guards could not run in this container (no `shellcheck` binary; the pinned `deadcode` tool needs a newer toolchain), so CI runs them.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix (docs) | A behaviour change with no ut-docs entry; the 1.14.x/1.15.0 rows set the precedent of recording core banding fixes. | **Fixed:** `fiscal-sign-ask.md` 1.15.1 row. |
| 2 | nit | The `SalesForTaxBandsInstant` doc still said "three fixed queries". | **Fixed.** |
| 3 | info (pre-existing) | LAN-sync `applyJournal` from a pre-ADR-0062 peer rebuilds one summed `service_charge` row. The primary's bands for that sale match its own persisted `tax_total`, but its per-rate split differs from the replica's. | Not a regression; tracked by ut-docs#986. |
| 4 | info | 0/1-charge sales are unchanged: one `ApportionChargesTax` call equals one `ApportionServiceChargeTax` call, and one row equals the scalar columns by construction (same transaction, same `[]ChargeInput`). Returns get rows through `refundCharges`. Rows and the scalar sum can't diverge. Charge weights match `computeSaleTotals`. | Verified. |

## Verdict

Safe to merge, in this order: core first, then ut-docs.
