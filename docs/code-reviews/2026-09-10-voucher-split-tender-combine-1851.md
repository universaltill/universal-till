# Code review: voucher + second payment method via the Split tab (ut-docs#1851)

**Date:** 2026-09-10
**Card:** ut-docs#1851 — "Split-tender panel has no way to combine a tracked
voucher with a second payment method"

## BA finding: the premise was stale

ut-docs#1851 was filed 2026-09-08 describing a real gap: the split-tender
panel only collected `{method, amount}` pairs with no `voucher_id` field, so
a tracked voucher payment couldn't be combined with cash/card in the same
sale.

A few hours later that same evening, ut-docs#1832 (PR `universal-till#937`,
merged 2026-09-09T03:25) shipped the cashier-facing voucher UI and, as part
of wiring `voucher_id` into the general Split-tab flow, closed this exact
gap as a side effect: `web/public/app.js`'s `addPayment()` now accepts a
voucher leg via the same `payments.push(payment)` mechanism used for every
other method, and `internal/pos/sales.go`'s `netPayments` already carries
running-sum-aware over-tender protection built for exactly this combination
(`outstanding := total.Sub(sum)`, where `sum` is the coverage from payments
*before* the current one in the loop).

Nobody went back to close #1851 once #1832 landed, and — critically —
nothing in the test suite (Go or Playwright) ever exercised a voucher leg
combined with a second-method leg in one sale; every existing voucher test
paid with the voucher alone or with cash alone. So the real remaining gap
was regression coverage, not a missing capability. No production code
changed in this PR.

## What shipped

- `internal/pos/voucher_sale_test.go`:
  - `TestCompleteSale_VoucherPlusCashSplitTender` — a voucher (balance 600)
    plus a cash leg (400) in one sale (total 1000): asserts success, correct
    sale total, the voucher fully drawn down by exactly its own leg (not the
    combined total), a single redemption ledger row of 600, and exactly 2
    persisted payment rows with the right method/amount/voucher_id split.
  - `TestCompleteSale_VoucherOvertenderIsRunningSumAware` (added after
    independent review, see below) — a cash leg (400) followed by a voucher
    leg (700) against a 1000 total: only 600 is actually outstanding once the
    prior leg is accounted for, so the voucher leg must be refused
    (`ErrVoucherOvertender`) and the voucher balance must stay untouched at
    700. This is the case that actually distinguishes the running-sum-aware
    cap from a naive "voucher amount <= sale total" check — the first test
    alone can't tell them apart, since it always places the voucher leg
    where the running sum is still zero.
- `e2e/tests/voucher-split-tender-combine-1851.spec.ts` (new) — drives the
  real browser: issues a voucher (balance 50) via the API, scans a
  120-minor-unit demo item, adds a voucher leg (0.50) and a cash leg via
  `#split-tender-fill` through the real Split-tab UI, submits, and asserts
  no error toast, "Sale completed.", 0 pending pills/basket rows, the cash
  leg's filled amount is exactly 0.70 (not the full amount due), and reads
  back `/api/vouchers/{code}` to confirm the voucher is debited by exactly
  its own leg (balance 0, status `redeemed`).

## Independent review (Opus, isolated worktree)

Spawned per `complexity:medium` routing (Sonnet wrote it, Opus reviewed).
Findings:

- **Medium (fixed in this PR):** the original Go test always placed the
  voucher leg first, where the running sum is zero — a mutation that
  replaced the running-sum-aware cap with a naive `total` cap left the
  entire package green, including the new test. Added
  `TestCompleteSale_VoucherOvertenderIsRunningSumAware` (voucher leg placed
  *second*, behind a partially-covering cash leg) to pin the actual
  property the card is about. Verified both ways: fails under the mutation
  (sale wrongly completes / voucher wrongly confiscated), passes on restored
  code.
- **Low (fixed in this PR):** the persisted-payments assertion checked "2
  distinct methods" via a map, which a duplicate-row bug for the same
  method could pass despite 3 real rows. Added an explicit
  `SELECT COUNT(*)` check ahead of the map read.
- **Low (fixed in this PR):** the e2e spec asserted pill *count* after
  `#split-tender-fill` but never the amount it actually filled — a
  regression that filled the full amount due instead of the true remainder
  would still pass every other assertion (the sale still completes, the
  voucher still zeroes out). Added a direct assertion that the amount field
  reads `0.70` after clicking Fill.
- Confirmed clean, no changes needed: voucher debit amount uses the leg's
  own amount everywhere (not the sale total); no `os.MkdirAll`/`paths.Data`
  concerns (no new file-writing code); test fixtures reused correctly, no
  duplicated setup; `watchConsole(page)` present; `afterEach` resets
  server-side state per `e2e/README`'s rule; no secret-shaped literals or
  real client/shop names in test data.

Independent TDD re-verification, done for real (not asserted): the reviewer
mutated `sales.go` to (a) debit the voucher for the sale total instead of
its own leg, and (b) drop the running-sum-aware over-tender cap entirely,
re-ran the tests against each mutation, confirmed the intended test failed
with a meaningful assertion error each time, then restored the original
file byte-for-byte and confirmed both tests pass again. Also mutation-tested
the e2e spec by dropping `payment.voucher_id` in `app.js` (the historical
ut-docs#1833 bug class) — the UI still reported success, only the
`/api/vouchers/{code}` read-back caught the missing debit, confirming the
spec isn't render-only.

## Verified beyond automated tests

- Full `go build ./...`, `gofmt -l .`, `go vet ./...` clean.
- Full `go test ./...` green (whole module, not just `internal/pos`).
- The new Playwright spec plus the full existing `voucher`/`split-tender`
  e2e group (20 specs) run together, all green — no regression in the
  neighbouring one-tap voucher-pay-button specs or the underpayment/i18n
  split-tender specs.
- `scripts/ci/guard-e2e-fixtures-import.sh` passes.

## Safe-to-merge verdict

Yes. Test-only change (no production code touched), all findings from
independent review fixed and re-verified, full gate green.

## Explicitly out of scope / deferred

- The one-tap `#pay-voucher-btn` pay-grid button's behavior when the
  voucher balance is *less* than the amount due (it clamps to the lesser
  amount and submits immediately, which underpays and is rejected by
  `netPayments` with no UI guidance toward the Split tab) is a separate,
  pre-existing UX question not introduced or worsened by this PR — noted
  here for visibility, not filed as a new card since it's a judgment call
  about a UX affordance, not a defect this coverage change is responsible
  for.
