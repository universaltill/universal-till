# Review: a basket discount no longer reaches a single-purpose voucher's VAT band (ut-docs#4233)

**Date:** 2026-10-13 · **Lane:** `lane:cloud-41` · **Author model:** Opus 5.5 · **Reviewer:** independent Fable subagent (different model)

## What shipped

- **universal-till** (`fix/4233-voucher-band-discount`):
  - `pos.VATLine` gains `SinglePurposeVoucher`.
  - Three places set it, one per path that bands a voucher being sold:
    - `SinglePurposeVoucherVATLine`: Z report, per-method tax bands, invoice, cloud sales upload.
    - `computeSaleTotals`: persisted `tax_total`, receipt.
    - `computeTotals`: live basket panel.
  - `VATBandsForSale` now prorates the sale discount by each band's **goods** gross.
  - Inclusive pricing re-derives only the goods part of a band. The voucher part keeps its issue-time gross and tax.
  - The largest-remainder pennies go to the highest band with goods worth more than 0.
  - A sale with no goods but a discount falls back to the old all-bands proration, so the bands still sum to the total.
- **ut-docs** `reference/contracts/fiscal-sign-ask.md`: the recommended `sale_discount` method now excludes single-purpose vouchers. Changelog 1.14.1, doc-only, no wire change.

**Example from the card** (inclusive): goods 100.00 @19% + voucher 50.00 @7%, discount 10.00.
- Before: 19% band 93.33 / VAT 14.91; 7% band 46.67 / VAT 3.06.
- Now: 19% band 90.00 / VAT 14.37; 7% band 50.00 / VAT 3.27.
- The inclusive `tax_total` goes from 17.97 to 17.64. Totals don't change.

## Findings and their outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (follow-up) | `ut-plugin-tax-de` ≤ v0.12.0 still apportions `sale_discount` over full band gross. A discounted sale that also sells a specific-item voucher is signed with a different per-rate split from the Z report. Totals still reconcile, so the plugin doesn't refuse it. | Contract doc corrected here; plugin fix filed as ut-docs#4240. |
| 2 | minor | A zero-gross goods line in a voucher's band could still take the largest-remainder penny, so the penny came off the voucher. | Fixed. `last` skips a band whose goods are worth 0 next to a voucher; sales without vouchers are unchanged. New test `TestVATBandsForSale_RemainderNeverLandsOnVoucherOnlyGross` failed first (VAT 7.97 vs 7.98), then passed. |
| 3 | minor | Doc: under exclusive pricing, `voucher_issues.amount` is net, not gross. | Fixed: the doc now gives the gross formula. |
| 4 | minor | Doc: core prorates the discount before the service charge is folded into the bands; a signer using payload gross can drift by a rounding unit. Pre-existing since 1.5.0. | Documented in step 1. |
| 5 | nit | Two `1.14.0` rows in the changelog. | Relabelled to `1.14.1`. |
| 6 | nit | The fallback comment named only direct CompleteSale callers; the tender with an explicit discount also reaches it. | Comment widened. |

The reviewer confirmed that **sales without vouchers give byte-identical results**: with no flagged line, every band has goods, `last` is the top band, and the formulas reduce to the old ones. Both invariants still hold:
- sum(band.Gross) = total
- sum(band.Tax) = `tax_total`

Every `VATLine` construction site was checked. Pool reuse in `computeTotals` is safe because each append is a full struct literal.

## Verified beyond automated tests

- **TDD:** I watched all 6 new tests fail against the unfixed code before writing the fix. The reviewer also hand-checked that they fail against the old code; examples: old 7% gross 4667 vs 5000; old `tax_total` 1797 vs 1764.
- **Gates:** gofmt, `go build ./...`, `go vet ./...` and golangci-lint are clean. `go test ./...` is green; `internal/plugins` only passes with `-timeout 1800s` under CPU contention and nothing there changed.
- **Guards:** data-access, i18n, core-neutral, kiosk-engine, no-showmodal, compliance-claims, competitor-naming, card-data-schema and deadcode-baseline-growth pass. `guard-deadcode-baseline` can't run here (its deadcode binary was built with go1.26). No Go symbols were added or removed; CI runs it.
- **No UI surface:** no strings, templates or help changed. The help (`vouchers.md`) already says a basket discount never reduces a voucher, and now the VAT split agrees with it. No UX pass needed.

## Not covered

- Historical sales already persisted with this shape keep their old inclusive `tax_total`; same policy as ut-docs#1114. Single-purpose voucher *items* shipped on 2026-10-13 (#4214), and no real shop is live.
- Signer side: ut-docs#4240.
