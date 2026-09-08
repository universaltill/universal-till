-- ADR-0084 (ut-docs#1716): atomic cross-till voucher redemption via an
-- idempotent primary-side reservation. A replica now RESERVES a tracked
-- voucher redemption on the primary (POST /api/sync/vouchers/{id}/redeem,
-- sync_vouchers.go) at tender time, which debits the primary's balance and
-- writes a 'redemption' voucher_transactions row keyed on the SAME sale id
-- the replica's own local sale, and its eventual journal replay, will carry.
-- That (voucher_id, sale_id) pair is the idempotency key that lets the
-- journal replay (pos.CompleteSale, via applyJournal) recognize a debit the
-- reservation already applied and skip it — closing the double-debit bug
-- that got the first mutating endpoint reverted (ut-docs#1668 round-2
-- review) — and lets a retried /redeem after a lost response return the
-- current state instead of debiting twice.
--
-- The app-level pre-check (data.POSRepo.VoucherRedemptionRecorded) runs
-- inside the same BEGIN IMMEDIATE transaction as the debit it guards, so
-- under this codebase's own writers it is already race-free; this partial
-- unique index makes the same rule a hard DB constraint, so no writer —
-- present or future, repository or not — can ever land two 'redemption'
-- rows for one (voucher_id, sale_id).
--
-- Partial (WHERE type = 'redemption') on purpose: an 'issue' row for the
-- same pair must keep coexisting with a redemption row, and pre-ut-docs#1053
-- ledger rows carrying a NULL sale_id never collide with each other (SQLite
-- treats NULLs as distinct in a unique index), so this is safe to apply to
-- any existing database — no data backfill, no shape a live ledger can be
-- in that this index refuses. Idempotent on a re-run via IF NOT EXISTS.

CREATE UNIQUE INDEX IF NOT EXISTS ux_voucher_tx_redemption_once
  ON voucher_transactions (voucher_id, sale_id)
  WHERE type = 'redemption';
