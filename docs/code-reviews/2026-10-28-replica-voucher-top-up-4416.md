# Review: gift voucher top-up from an additional till (ut-docs#4416)

**Date:** 2026-10-28 · **Lane:** `lane:cloud-41` · **Branch:** `feat/4416-replica-voucher-top-up`
**Built by:** Sonnet 5.5 (Dev subagent), with follow-up fixes by Opus 5.5 (orchestrator) · **Reviewed by:** Fable (independent, different model; hard card that touches money)

## What shipped
- **Version gate (LAN-sync contract 1.13.0).** `GET /api/sync/vouchers/{id}` answers `accepts_top_up: true`. A wrapper type is used, so `/redeem` and `/adjust` are unchanged on the wire.
  - An additional till's `POST /vouchers/{id}/top-up` asks the main till first (`lookupVoucherOnPrimary`). It refuses with the reworded `vouchers.top_up.replica_note` (409) when the main till is unreachable or its answer lacks `accepts_top_up`. It refuses with not-found (404) when the main till has no such voucher.
  - The void, single-purpose and ceiling checks run on the **main till's** row.
  - Only after every check passes does `EnsureVoucherLocalRow` seed the local mirror and the basket line get added. Checkout itself never calls the network.
- **Forced credit.** New `pos.SaleInput.ForceVoucherTopUps` → `data.TopUpVoucher(..., force)` skips the void and ceiling checks. An unknown or single-purpose voucher still refuses, and a void voucher stays void.
  - It is set on journal replay (`applyJournal`: the remote sale already happened) and on an additional till's own tender (the main till checked at line-add; the local mirror may be stale).
  - `ForceVoucherTopUps` is not a wire field, and a body `issue_vouchers[].top_up` is still refused (the #4252 M1 fix).
- **Replay.** A forced credit onto a void or over-ceiling voucher raises a Problem (`warnIfTopUpVoucherUnsound`). Two cases are quarantined instead of 422-ing the batch: "unknown voucher on top-up replay" (mapped to the reworded existing `sync.quarantine_reason.unknown_voucher_redemption`) and "single-purpose voucher on top-up replay" (raw-English fallback at the time; its own key since ut-docs#5134).
- **Receipt balance on a mirror.** `VoucherTopUpsInSale.balance_after` keeps the ledger sum wherever the voucher's `issue` row is on this till, so the main till is unchanged. A mirror has no issue row, so it reads backwards from the current balance minus later rows.
- **Voucher page on an additional till.**
  - The Top up form shows (the main-till-only note is gone), and `possible`/`void` are judged on the main till's row.
  - A voucher this till has no row for opens from the main till (empty local history, nothing written). A list search for its full number that finds nothing locally asks the main till for it.
- **Void of a forced credit.** `reverseTopUpsInSale` also reverses a top-up on a void voucher, which stays void.
- Help `vouchers.md` (en/de/tr/fa/ar), the reworded `replica_note` (en/tr/fa/ar), README, docs-shots regenerated. Contract doc: ut-docs `reference/contracts/pos-lan-sync-journal.md` 1.13.0.

## Findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The form was reachable only for vouchers the replica already mirrored (detail page and list are local). `possible` was judged on a stale local row. | **Fixed.** Detail and list fall back to the main till; the form is judged on the main till's row. `TestVoucherPages_ReplicaFallsBackToMainTill` failed before the fix (detail 500; list missing the row). |
| 2 | major (pre-existing class) | A replica void of an already-pushed top-up (or issue) sale never reaches the main till, so the credit stays there. | **Deferred → ut-docs#5133 (p2).** Voids aren't journaled for any voucher effect today; it needs its own design (refuse vs. proxy like `/adjust`). |
| 3 | minor | A forced credit onto a void voucher made that sale un-voidable, with "already redeemed" wording. | **Fixed.** The reversal admits `status IN ('active','void')` and keeps void. `TestReverseTopUp_ForcedCreditOnVoidVoucherIsVoidable` failed before the fix. |
| 4 | minor | The single-purpose top-up quarantine reason has no locale key. | **Deferred → ut-docs#5134.** Only a malformed peer can produce it. The new unknown-voucher case is mapped and added to `TestPermanentJournalFailureReason_AllReasonsHaveALocaleKey`. |
| 5 | minor | On the main till, `balance_after` after a forced credit onto a voided voucher shows the ledger sum, not the real balance (a void sets 0 with no row). | **Accepted.** A pre-existing caveat, documented on the function; this only arises after a Problem has already been raised. |
| 6 | nit | `cashTender(...).Code == 200` is not a success check; the mirror test's comment promised a void case it didn't run. | The void case was added to `TestVoucherTopUpsInSale_MirrorWithoutIssueRow`. The balance assertions are what catch a refused tender (the reviewer's mutation M2 confirmed this). |
| 7 | nit | A held basket with a top-up line resumed on a *different* replica fails at tender (no mirror) with the top-up toast. | **Accepted.** It is a refusal, not a loss; the cashier removes the line. |

Also caught in the Dev hand-back, before review, and fixed: the replica's top-up receipt showed the ledger sum of a history-less mirror (25.00 instead of 45.00). `TestVoucherTopUpsInSale_MirrorWithoutIssueRow` failed with "BalanceAfterMinor:2500, want 4500" before the fix.

## TDD re-verified
I reverted each fix in turn and saw its test fail:
- the `balance_after` hybrid;
- the void-reversal guard;
- the detail and list fallbacks.

The reviewer ran three mutations: dropping the `accepts_top_up` gate, `ForceVoucherTopUps = false` in completeTender, and the `balance_after` revert. All were caught. The Dev showed that `ForceVoucherTopUps = false` in `applyJournal` fails both replay tests.

## Verified beyond automated tests
- The Playwright voucher specs passed against the real binary (11/11): `voucher-top-up-4252`, `vouchers-page-4251`, `vouchers-filter-url-4257`, `gift-voucher-item-4214`. This includes the Top up section at 360px in en and fa (RTL).
- `make docs-shots` was regenerated and its guard is fresh.
- **Not looked at:** a driven two-till run. There is no multi-till e2e harness. The replica paths are covered by handler tests against a real second till (an httptest primary on its own migrated DB), including the end-to-end case where the journal credits the main till exactly once when replayed twice.

## Verdict
Safe to merge. No path credits the main till twice or zero times, and none forces a credit from a request body.
