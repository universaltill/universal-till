# Review — VAT band discount remainder skips a zero-goods band (ut-docs#4275)

**Date:** 2026-10-14 · **Lane:** `lane:cloud-41` · **Author model:** Opus 5.5 ·
**Reviewer:** independent Fable subagent (different model from the author)

## What shipped

- `internal/pos/vat_breakdown.go` — `VATBandsForSale` gives the
  largest-remainder minor units of a sale discount to the highest band
  whose **goods gross is > 0** (`b.Gross-b.voucherGross > 0`). Before, a
  band whose only goods line was fully line-discounted (gross 0) and that
  held no voucher qualified too, so it took the remainder and went to
  −0.01. The Z report and invoice then differed from what
  `ut-plugin-tax-de` v0.12.1 signs by a cent on two rates.
- `internal/pos/vat_band_remainder_test.go` — the card's repro (1.00 @7%,
  2.00 @10.7%, 0.00 @19%, 1.00 discount) in inclusive and exclusive mode:
  expected 0.67 / 1.33 / 0.00 gross (inclusive) and 0.68 / 1.32 / 0.00 net
  (exclusive, prorated by gross share 1.07 vs 2.21). Also asserts no
  negative band, Net+Tax == Gross per band, and the bands sum to what the
  customer paid.
- ut-docs `reference/contracts/fiscal-sign-ask.md` 1.14.2 (doc-only): step 3
  no longer limits the zero-goods exclusion to "next to a voucher".

## TDD

The new test failed on the unfixed code (inclusive: 19% band −0.01,
10.7% 1.34; exclusive: 19% −0.01 net, 10.7% 1.33) and passes with the fix.
Re-verified by the orchestrator (revert → fail → restore → pass) and
independently by the reviewer with a `go test -overlay` of the old file.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor, pre-existing | Floor-then-remainder can still push a top band with tiny goods negative (1.00 @7%, 2.00 @10.7%, 0.01 @19%, 3.00 discount → 19% band −0.01). The signer follows the same contract, so no drift today. | Out of scope — filed ut-docs#4277 (core + contract + tax-de together). |
| 2 | nit | Changelog row says "one cent per rate"; the repro moves two bands by a cent each. | Accepted — that is what the wording means. |
| 3 | nit | Review record missing. | Fixed — this file. |

Checked and fine: `last` can't be −1 while the distribution loop runs
(`goodsGrossSum > 0` implies a band with goods > 0; the no-goods fallback
clears `voucherGross`); every #4233 voucher test passes under old and new
code (the new predicate is a strict subset of the old one);
`buildFiscalSignPayload` doesn't prorate the discount itself, and the
service-charge apportionment is a separate rule. `computeSaleTotals`
consumes these bands, so a new sale of the repro shape persists
`tax_total` one cent higher. That is intended, and existing sales are not
backfilled (#1114 policy).

## Gate

`gofmt` clean, `go build ./...`, `go vet ./internal/pos/`,
`golangci-lint run ./internal/pos/...` (0 issues), guards data-access,
core-neutral, i18n, kiosk-engine pass, full `go test ./...` (see PR).
No UI surface touched, so no e2e or visual check is needed. No help topic
changes: the user manual doesn't describe the remainder rule.

**Verdict:** safe to merge.
