# Review — reader-reported tip folded into payments.amount (ut-docs#2571)

**Date:** 2026-09-26 · **Branch:** `fix/2571-reader-tip-in-amount` · **Author:** Opus 5.5 (lane:cloud-24) · **Reviewer:** Fable (independent, different model)

## What shipped

Core had two conventions for whether `PaymentInput.Amount` includes its tip:

- **Stored:** `payments.amount` includes the tip and `tip_amount` is the breakdown. This is how `InsertPayment`, `data.EODTip`, the per-method tax-band queries (`amount - change_given - tip_amount`) and both receipts treat it.
- **Reader-reported** (`payment.<key>.authorize` → `tip_amount`, e.g. the SumUp reader): only `TipAmount` was overwritten, so `Amount` stayed at the requested value. The reader had actually charged the requested amount plus the tip. As a result, EOD per-method takings missed reader tips and the tax bands under-counted those card legs by the tip.

`completeTender` now calls `applyPluginReportedTip`, which adds the reader tip to both `Amount` and `TipAmount`:

- A tip the request itself carried already sits inside `Amount` and was charged, so the reader's tip adds to it rather than replacing it.
- It refuses a tip that would overflow `Amount`.
- It refuses any tip on a tracked voucher leg.

Doc comments in `internal/pos/sales.go` and the `/api/pos/tender` `tip` field now state the one convention.

The contract changes are in ut-docs (`docs/2571-tip-convention`):

- `fiscal-sign-ask` 1.11.0: `amount` always includes the tip, so for an exactly paid sale `Σamount = total + Σtip`.
- `architecture/wasm-runtime.md`: the `tip_amount` authorize response now adds to both `Amount` and `TipAmount`.

## Tests (TDD)

- `TestTenderHandler_AppliesPluginReportedTipFromAuthorizeResponse` (cashier path) now also asserts `amount = 120 + 150`.
- `TestSelfOrderShop_CheckoutAppliesPluginReportedTipFromAuthorizeResponse` (kiosk path) now also asserts `amount = sale total + 150`.
- New table test `TestApplyPluginReportedTip` covers:
  - no request tip
  - zero tip
  - request tip plus reader tip (additive)
  - overflow refused
  - voucher leg refused

All three tests failed against the old behaviour ("want amount 270 … got 120", "want amount 534 … got 384"). The author checked this, and the reviewer re-checked it independently in a separate worktree. With the fix restored, they pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The plugin-facing contracts change meaning: the fiscal.sign.ask payment `amount` and the authorize `tip_amount` are now "on top, core adds it". | **Fixed** in the same session: `fiscal-sign-ask` 1.11.0 changelog and `wasm-runtime.md`. `ut-plugin-tax-de` v0.7.0 reconciles against `total`, so it stays correct. Asserting instead of inferring is ut-docs#2976. |
| 2 | minor | Apart from the overflow check, a reported tip has no bound. A buggy plugin's huge tip now also inflates `amount`, EOD and the signed record. | **Accepted.** An unbounded `TipAmount` is pre-existing (2026-08-01 review), plugins are Ed25519-verified, and any cap could drop a genuine large tip. |
| 3 | minor | A tip reported on a tracked voucher leg inflated `Amount` before the primary reservation, so the wrong debit was reserved and then released when `CompleteSale` refused the sale. | **Fixed:** `applyPluginReportedTip` ignores tips on voucher legs, with a test case. |
| 4 | nit | The second overflow clause is redundant. | Kept; it is harmless and fails closed. |
| 5 | nit | The `trigger_event` publish now sends `amount` including a reader tip. | Consistent with request-carried tips and covered by the convention statement. |

The reviewer found no double counting in:

- EOD `EODMethod.In` and the cash reconciliation
- the tax bands
- the sales aggregates, worker allocation and yüzde usulü pool
- both receipts
- refunds (they are line-based)
- LAN journal replay (fields verbatim)
- the idempotency key (built before the mutation)
- the OKC leg
- the kiosk path

## Deferred

- **ut-docs#2975:** `netPayments` coverage still counts the tip toward the total. Changing that needs journal-version gating, because older replicas' reader-tip sales would be rejected with 422 on replay.
- **ut-docs#2976:** `ut-plugin-tax-de` should assert `Σamount = total + Σtip`.

## Gate

- Passed: `gofmt`, `go build ./...`, `go vet`, full `go test ./...`, `golangci-lint` (0 issues).
- Passed: every `ci.yml` build guard except the two below.
- Failed only for local-environment reasons:
  - `guard-shellcheck-version`: no shellcheck binary.
  - `guard-deadcode-baseline`: flags `internal/logging/file.go`, which this change doesn't touch, because GTK is missing for `cmd/unitill-desktop`.
- There is no UI change, so no screenshots and no help-topic change. `web/help/en/reports.md`'s "card tax rows total less than takings by the day's card tips" is now also true for reader tips.

**Verdict:** safe to merge.
