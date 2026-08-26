# Code review: voucher issue/redemption over LAN sync journal (ut-docs#1053)

**Date:** 2026-08-26
**Author:** pipeline (Dev at Fable, complexity:hard)
**Reviewer:** independent Opus subagent, isolated git worktree
**PR:** feat/1053-voucher-lan-sync-replication

## What shipped

`applyJournal` (the primary-side LAN-sync journal replay path,
`internal/pages/sync_sales.go`) never reconstructed a replica's voucher
issue/redemption data, and `payments` had no column to record which voucher
a payment redeemed even on a single till. A voucher issued or redeemed on a
replica silently failed to replicate: `total` missing the voucher's face
value, `voucher_issue_total = 0`, no `vouchers`/`voucher_transactions` rows.

- **Migration 072** (`internal/db/migrations/072_payments_voucher_id.sql`):
  `payments.voucher_id`, mirrored onto `payments_archive` per the migration
  069 precedent, and `resetArchiveTables` updated so the column round-trips
  through a transaction-history reset/restore.
- `POSRepo.InsertPayment` gains a `voucherID` param, persisted.
  `POSRepo.GetSaleDetail` returns it on `SaleDetailPayment.VoucherID` and
  returns the new `SaleDetail.VoucherIssues` (joined from
  `voucher_transactions`+`vouchers`, both deliberately FK-less soft
  references per migration 068's own header). Both new wire fields are
  `omitempty`, matching the `TipRecipient`/`ServiceChargeTaxBasisBP`
  convention — an old peer's payload still degrades gracefully.
- `applyJournal` reconstructs `VoucherIssues` and each payment's `VoucherID`
  from the journal entry.
- `DebitVoucherForRedemption` gains a `force bool` param. Under `force`, an
  overdraft debits anyway (balance goes negative) instead of rejecting —
  the LAN-sync replay path's genuine-offline-double-spend case, where the
  money already moved at the remote till. This follows the exact
  `AllowNegativeInventory`/`warnIfStockNegative` precedent already
  established for stock. `pos.SaleInput.AllowVoucherOverdraft` is set
  `true` only inside `applyJournal`'s construction of the replay `in` —
  never reachable from a normal tender/checkout flow. A forced-negative
  debit during replay surfaces as a Problems-panel warning
  (`logging.L().Warnf`, an internal diagnostic, not a user-facing string —
  no i18n key needed).
- Journal contract doc bumped to 1.3.0 in the sibling `ut-docs` repo
  (`reference/contracts/pos-lan-sync-journal.md`), documenting both new wire
  fields and the overdraft-bypass semantics.

## Independent review — findings and disposition

Reviewed by a fresh-context Opus subagent (complexity:hard → Fable builds,
Opus reviews, per the pipeline's model-routing rule), isolated in its own
git worktree so its revert-then-restore TDD re-verification never touched
the shared checkout.

### BLOCKER — exact-drain double-spend hard-rejected instead of force-applying — **FIXED**

`DebitVoucherForRedemption`'s pre-read gate (`status != "active" →
ErrVoucherNotActive`) ran unconditionally, before the `force` branch. But
draining a voucher's balance to exactly zero flips its status to
`'redeemed'`. So the single most likely double-spend shape — a customer
spending a voucher's *entire* remaining value at till A, then again at till
B, both offline before either synced — had till A's replay drain to 0
(status → `'redeemed'`), and till B's replay then hit `ErrVoucherNotActive`
before the balance check (and `force`) ever ran. `applyJournal` returned an
error, `registerSyncSales` rejected the whole batch (`422`), and
`syncPushTick` never advances `sync.push_cursor` past a failed entry — that
replica would rebuild and re-fail the identical batch every push tick,
forever, with no operator-visible escape. This directly contradicted the
1.3.0 contract doc's own Guarantees wording, which already promised the
force-applies behavior the code didn't actually implement.

**Fix:** the pre-read gate and the guarded UPDATE's predicate both now
accept `status IN ('active', 'redeemed')` under `force` — only `'void'`
stays a hard reject even under force, since a voided voucher is a
different, worse problem than a balance/status race caused by replay
ordering. Regression coverage added at both layers:
- `internal/data/voucher_repo_test.go`'s `TestVoucherRepo_DebitForceAllowsOverdraft`
  — `GS-EXACTDRAIN` (drain to exactly zero, then force-redeem again,
  expect success) and `GS-VOIDED` (force still hard-rejects a genuinely
  voided voucher).
- `internal/pages/sync_sales_voucher_test.go`'s new
  `TestApplyJournal_ExactDrainThenRacingRedemptionForceApplies`, driving
  the real `applyJournal` replay path end-to-end for the exact-drain shape,
  alongside the existing `TestApplyJournal_DoubleRedemptionRaceForceAppliesAndSurfacesProblem`
  which covers the partial-drain shape.

Both the reviewer's original revert-then-restore TDD re-verification (which
confirmed the *pre-fix* tests genuinely pinned the intended `force`
behavior — see below) and a fresh full run of the affected packages after
the fix are green.

### REAL-BUT-DEFERRABLE — unknown-voucher and duplicate-voucher-code replay also wedge the journal — **deferred, follow-up card filed**

Two related but distinct failure modes the reviewer found, both landing on
the same underlying gap: **any `applyJournal` error currently halts a
replica's replication with no operator-visible signal and no escape
hatch**, which this card's own new code makes easier to trigger (voucher
data now actually replicates, where before it silently didn't):

1. A pre-1.3.0 replica's already-issued vouchers are unknown to the primary
   (their issue never replicated). The first post-upgrade redemption of any
   such voucher hits `ErrVoucherNotFound` — a deliberate, correct hard
   reject as a *policy* — but with no backfill or quarantine/skip path in
   this diff, it wedges that replica the same way.
2. Voucher ids are operator-supplied codes (not generated ids), so two
   tills issuing the same pre-printed/colliding code offline hits a
   `vouchers` primary-key conflict on replay, rolling back the whole
   journal entry — a failure mode that didn't exist before this diff
   (voucher issues never replicated pre-1.3.0) and isn't covered by the
   1.3.0 contract doc.

Both are real and worth fixing, but they're a broader "journal poison-entry
handling" gap (no replica-side skip/quarantine mechanism exists at all
today, for ANY poisoned entry, not just a voucher one) rather than this
card's scope of "make voucher data replicate." Filed as
**universaltill/ut-docs#1127** (Backlog, `needs-info`-free, unassigned) for
a follow-up cycle, rather than expanding this PR to redesign journal error
handling.

### Nitpicks — fixed / accepted

- The Problems-panel warning printed the overdrawn balance as raw minor
  units (`%d`, e.g. `-600`) rather than a formatted amount. Fixed to use
  `money.Money.String()` (e.g. `-6.00`), matching how the rest of the
  codebase renders amounts in log/diagnostic text.
- The force path's lost-race branch (`n != 1` under `force`, meaning a
  concurrent void won the race between the pre-read and the guarded UPDATE)
  has no dedicated test — accepted as-is; it's a narrow concurrency window
  that's hard to drive deterministically without more invasive mocking than
  its risk warrants, and the guard predicate itself is exercised by every
  other `force` test.
- `GetSaleDetail`'s new voucher-issues join is an inner join against
  `vouchers` — theoretical only, since vouchers are never deleted in this
  codebase today.

## Verified beyond automated tests

- **TDD re-verification (revert → fail → restore → green), performed by
  the reviewer inside its isolated worktree**: temporarily forced `force`
  to a no-op inside `DebitVoucherForRedemption`. All three
  force-dependent tests failed with the real, expected
  `ErrVoucherInsufficientBalance` error (not a compile error or a
  tautological pass) — `TestVoucherRepo_DebitForceAllowsOverdraft`,
  `TestCompleteSale_VoucherOverdraftForceAllowedOnReplay`, and
  `TestApplyJournal_DoubleRedemptionRaceForceAppliesAndSurfacesProblem`.
  Restored, confirmed green again, confirmed `git diff` against the
  reviewed commit was empty afterward (the worktree left no residue).
- **The blocker itself was proven with a throwaway probe test** driving the
  real `applyJournal`, deleted after confirming the failure mode
  (`status redeemed → ErrVoucherNotActive` on the second replay) — not
  inferred from reading the code.
- Full repo test suite (`go test ./... -count=1`) green after the fix —
  every package, not just the three touched — confirming no sibling-package
  regression from the `InsertPayment`/`DebitVoucherForRedemption` signature
  changes.
- `gofmt -l .` silent, `go build ./...` and `go vet ./...` clean.
- `scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` both
  pass — all new SQL lives in `internal/data`/`internal/db`, no new
  user-facing strings introduced (the Problems-panel warning is an internal
  diagnostic, same convention as `warnIfStockNegative`).
- Scoped `-race` runs (`-run 'Voucher|InsertPayment|GetSaleDetail'` etc.)
  on the touched tests are clean in `internal/data`, `internal/pos`, and
  `internal/pages`. A full-package `-race` run of `internal/data`/
  `internal/pages` hits this sandbox's known, pre-existing 20-30+ minute
  timeout (tracked separately as ut-docs#1119/#1120, not introduced by this
  change) — not attempted as the release gate here; the scoped runs plus a
  full non-race run of the whole repo are the verification basis instead.
- Backend-only change: no `web/ui/**`, `web/locales/*.json`, or
  `web/help/**` touched, so the UX-guidelines checklist and manual-update
  requirement don't apply — confirmed by `git diff --stat` (all 24 files
  under `internal/`) rather than assumed.
- No real client/shop name used in any new test/seed data (`Sample Holder`,
  `GS-*` voucher ids, generic amounts).

## Safe-to-merge verdict

**Safe to merge** after the blocker fix above. No blocking findings remain;
the two deferred findings are tracked as ut-docs#1127 and are pre-existing
gaps this card's own success (voucher data now actually replicating) makes
newly reachable, not regressions this diff introduces on a previously-safe
path.
