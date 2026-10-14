# Review: fiscal sign — service charge out of the discount weights (ut-docs#4300)

Date: 2026-10-14 · Lane: `lane:cloud-41` · Built by Opus 5.5, reviewed by Fable (different model).

## What shipped (three repos, one change)

- **universal-till** (`fix/4300-fiscal-charge-per-rate`): each `fiscal.sign.ask` `vat_breakdown` entry carries `service_charge_net` / `service_charge_tax`, filled from the existing `pos.ApportionChargesTax` loop in `buildFiscalSignPayload`. Each field is `omitempty`. Nothing is re-derived.
- **ut-docs** (`fix/4300-fiscal-charge-per-rate`): contract `fiscal-sign-ask.md` 1.16.0.
  - Field notes and example updated.
  - § `sale_discount` step 1 now says to take the charge part out before weighting and capping; step 3's exclusive cap names `service_charge_net`.
  - Changelog row and known-consumers entry added.
- **ut-plugin-tax-de** v0.13.0 (`fix/4300-charge-out-of-discount-weights`): `discountedGrossByRate` works on each band less its charge part and adds the part back afterwards. A part that is negative, larger than its band, or doesn't sum to `service_charge` is refused with `cannot-sign`. A payload from before 1.16.0 (no parts) keeps the v0.12.2 behaviour. README and CLAUDE.md updated.

Option (a) from the card was chosen over (b). Under ADR-0136, (b) would refuse the tender on every German sale that has both a discount and a service charge.

## Verification

- TDD: new core test `TestFiscalSignPayload_ChargePartPerRate` failed to compile before the fields existed. New plugin tests failed on the old code: exclusive 5.40/12.00 vs core's 5.39/12.01, inclusive 9.98/4.28 vs 9.97/4.29, charge-only band 4.95/1.09 vs 4.85/1.19, and the 4 inconsistent-part cases returned nil. The real-wasm `TestFiscalSignAsk_DiscountWithServiceChargeSignsCoreSplit` failed with `receipt.go` reverted. The reviewer re-ran all of these independently in a worktree.
- Expected figures were taken from core's own `buildFiscalSignPayload` and `VATBandsForSale` for the same sales, not computed by hand.
- Reviewer fuzz: `buildFiscalSignPayload` JSON → `ParseRequest` + `BuildReceipt` compared with `VATBandsForSale` over 2 × 300k random sales covering inclusive/exclusive pricing, 0/7/10.7/19 %, line discounts, zero-price lines, both voucher kinds, and flat-basis charges.
  - ≤1 charge with discount ≤ goods: **290,109/290,109 matched**, with no false `cannot-sign`.
- Gates: plugin `go vet`, `go test ./...` (incl. wasmrun), `build.sh`, `validate.sh`. Core `go build ./...`, `gofmt`, `go test ./...`. `internal/plugins` exceeds the local default 600 s timeout; CI already runs it with a wider timeout (ci.yml), and it was re-run locally with `-timeout 40m`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Contract and core comment said both parts are "omitted when the band carries no charge". In fact per-field `omitempty` drops `service_charge_tax` whenever it is 0 (e.g. 0 % bands). | **Fixed**: wording is now "each is omitted when zero; read absent as 0". |
| 2 | nit (pre-existing) | With 2+ charges, `VATBandsForSale` bands the summed charge once while the payload apportions per charge. Z report and signer differ even without a discount. | Doc qualifies "one service charge". **Filed ut-docs#4301.** |
| 3 | info | The negative-band refusal now applies to the goods part. A discount larger than the goods next to a charge is refused (v0.12.2 signed it). Unreachable from the till, because charges are computed on the post-discount subtotal. | Accepted: matches "never sign a wrong receipt". |
| 4 | nit | Plugin review record missing. | Fixed (this file is copied into each repo). |

## Verdict

Safe to merge, in this order: core → ut-docs → plugin. The plugin works with or without the core fields.
