# Review — VAT band discount share capped at the band's goods (ut-docs#4277)

**Date:** 2026-10-14 · **Lane:** `lane:cloud-41` · **Author model:** Opus 5.5 ·
**Reviewer:** independent Fable subagent (different model from the author)

## What shipped

- `internal/pos/vat_breakdown.go` — the sale-discount split moved into
  `splitSaleDiscount(d, weights, caps)`. Weights stay each band's goods
  gross (single-purpose vouchers excluded, #4233). Each floor share is
  capped at the goods it comes off (goods gross inclusive, goods **net**
  exclusive, since exclusive discounts the net base). The leftover minor
  units fill the bands from the highest rate down, each up to its cap. Any
  rest (a discount larger than every band's goods) goes on the highest band
  with goods gross > 0, so the shares always sum to the discount. Before,
  the whole remainder went on the highest band carrying goods, uncapped:
  1.00 @7%, 2.00 @10.7%, 0.01 @19%, 3.00 off left the 19% band at −0.01
  (and exclusive pricing also left the 10.7% band's net at −0.01).
- `internal/pos/vat_band_cap_test.go` — the card's repro in inclusive and
  exclusive mode (1 / 0 / 0 left per band, no negative band, Net+Tax ==
  Gross, bands sum to what the customer paid); a voucher sharing the top
  band keeps its own 500 / 80; an over-discount still sums.
- ut-docs `reference/contracts/fiscal-sign-ask.md` 1.15.0 (doc-only, signer
  must follow): step 3 describes the capped fill. Plugin follow-up:
  ut-docs#4282.

## TDD

All three new tests failed on the unfixed code (inclusive 19% = −1;
exclusive 10.7% net −1 and 19% −1; voucher band 499; over-discount 7% =
−33) and pass with the fix. Re-verified by the orchestrator (old file
restored → fail → new file → pass) and independently by the reviewer.

## Findings

1. Low — comments and the contract said the basket "never" produces an
   over-discount. It caps a discount at the goods only while a voucher is
   being sold (#4214); a fixed-amount discount larger than the goods still
   reaches `VATBandsForSale` (tax and total are clamped to 0 afterwards).
   Pre-existing behaviour, handled correctly by the new step 3. **Fixed:**
   wording corrected in `vat_breakdown.go`, the test comment and the
   contract (which now says such a sale doesn't reconcile).
2. Low — the contract said the rest goes on "the highest band carrying
   goods"; code uses goods gross > 0. **Fixed** in the contract.
3. Nit — contract "Last updated" not bumped. **Fixed.**

## Verified beyond the unit tests (reviewer)

- 2,000,000 random `splitSaleDiscount` cases: shares always sum to d;
  within [0, cap] whenever d ≤ Σcaps; zero-weight bands get nothing; and
  **identical to the old split in every case where the old result was
  already valid** — ordinary sales are unchanged.
- 300,000 random baskets through `VATBandsForSale` (both modes, vouchers,
  service charges): no negative Net/Tax/Gross, Net+Tax == Gross, sums hold.
  Scratch files removed.
- `go build ./...`, `go vet`, `golangci-lint` (0 issues), `go test ./...`,
  data-access / core-neutral / i18n guards. `guard-deadcode-baseline` can't
  run in this container (its tool is built with Go 1.26; it fails the same
  on `main`) — CI runs it.

No UI surface touched, no locale keys, no migration.

**Verdict:** safe to merge.
