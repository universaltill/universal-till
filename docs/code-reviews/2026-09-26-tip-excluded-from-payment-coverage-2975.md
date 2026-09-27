# Review: a tip no longer covers the sale total (ut-docs#2975)

**Date:** 2026-09-26 · **Branch:** `fix/2975-tip-coverage` · **Author lane:** lane:cloud-24 (Opus 5.5) · **Reviewer:** independent subagent, Fable (different model from the author)

## What shipped

- `pos.netPayments` now counts `Amount − ChangeGiven − TipAmount` toward the total, the same net the per-method tax-band queries in `internal/data/pos_repo.go` use. Since ut-docs#2571 `payments.amount` includes the tip, so before this change a tip counted as sale money. Example: sale 1000, card leg 950 plus a reader tip of 50, stored as 1000/50, passed coverage.
- A leg whose tip is larger than `amount − change` is refused, both in `netPayments` and with a 400 at the `/api/pos/tender` boundary, next to the ut-docs#1764 change check.
- `pos.CheckPaymentCoverage` is a DB-free copy of the coverage check, including CompleteSale's variant `ItemID` normalization, done on a copy of the lines. `completeTender` calls it after the fiscal gate and the OKC-leg check, but before `fiscal.sign.start`, the `payment.<key>.authorize` loop and the voucher write-through. An uncovered tender is refused before any card is charged. Before, CompleteSale refused it only after the reader had taken the money.
- `SaleInput.LegacyTipCoverage` is set only by `applyJournal`, following the AllowNegativeInventory/AllowVoucherOverdraft precedent. It keeps the old `Amount − ChangeGiven` rule for LAN journal replay. Without it, a replica older than #2571 (tip stored outside `amount`) would be refused with a 422 and its cursor would stall on a poison entry. The strict rule applies to every live tender path (cashier, kiosk, refunds, returns). ut-docs `reference/contracts/pos-lan-sync-journal.md` documents it. There is no wire change and no version bump.
- A net-sum overflow guard in `netPayments` (review nit).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The new tip-exceeds-amount refusal fell through to the generic `pos.toast.tender_failed`, unlike the change check, which returns a 400 at the boundary | **Fixed**: 400 `invalid tip amount` in the tender handler, with a test |
| 2 | minor | The `CheckPaymentCoverage` doc claimed it ran "CompleteSale's payment validation" | **Fixed**: the comment now names only what it runs |
| 3 | nit (pre-existing) | `sum.Add(applied)` across legs had no overflow guard | **Fixed**: guard + `TestNetPayments_RefusesOverflowingSum` |
| 4 | nit | The uncovered-tender test asserts "not success", not the toast key | Accepted: e2e `split-tender-underpayment-921` asserts the rendered error toast on the same path |

The reviewer confirmed the following:
- The pre-check computes the same total CompleteSale does. The only mutation that affects totals (variant ItemID) is mirrored.
- A reader-reported tip grows `Amount` and `TipAmount` equally, so authorize can't change the coverage answer.
- Refund, return and kiosk callers carry no tips, so they can't be newly refused.
- Legacy replay coverage is never stricter than a replica's own check.

## Verified

- **TDD, re-verified by reverting the fix:** with tip-subtraction and the pre-check disabled, `TestCompleteSale_TipDoesNotCoverTotal`, `TestCompleteSale_RejectsTipExceedingAmountLessChange`, `TestCheckPaymentCoverage/tip_inside_amount_short` and `TestTenderHandler_UncoveredTenderRefusedBeforeAuthorize` all fail ("authorize ran 1 time(s)"). With `LegacyTipCoverage` forced false, the existing `TestApplyJournal_MissingTipRecipientDefaultsToEmployee` and `…ServiceChargeReplayNeverReAsksChargePolicy` fail with "do not cover total", which proves the replay flag is load-bearing. All pass once the fix is restored.
- **Gate:** `gofmt -l .` is empty; `go build ./...` and `go vet ./...` are clean; `golangci-lint run ./...` reports 0 issues; `go test ./...` passes (70 packages). Every `scripts/ci/guard-*.sh` in the `build` job passes except `guard-shellcheck-version.sh`, which is environmental: the sandbox has no `shellcheck` binary, and this diff touches no shell.
- **e2e (Playwright, real till):** `sale.spec`, `split-tender-underpayment-921`, `split-tender-i18n-925` (en + fa/RTL), `voucher-split-tender-combine-1851` and `tender-panel-reachable` all pass (13/13). The underpayment spec's till log now shows the refusal coming from the pre-authorize check.
- **Visual:** no UI surface changed (same toast key, no new strings), so there were no screenshots to review.

## Verdict

Safe to merge. No deferred items.
