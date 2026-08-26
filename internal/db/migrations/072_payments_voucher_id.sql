-- 072: voucher_id on payments (ut-docs#1053). Which tracked voucher a
-- 'voucher'-method payment redeemed. pos.PaymentInput.VoucherID (ut-docs#1008)
-- already drives the balance debit and the voucher_transactions 'redemption'
-- row, but was dropped before persistence — so nothing could answer "which
-- payment row redeemed this voucher", and the LAN-sync journal (whose payload
-- IS GetSaleDetail's output) could not carry a redemption to the primary at
-- all: the replayed sale landed with an untracked generic voucher payment,
-- no debit and no redemption row. NULL/empty for every non-redemption
-- payment and for all pre-existing rows — correct, since those payments
-- redeemed no tracked voucher. Like vouchers.issued_sale_id (migration 068)
-- this is a soft, informational reference with NO FK to vouchers(id): the
-- voucher tables deliberately live outside reset-archive so a liability
-- survives a transaction-history reset, and no table archived by reset may
-- gain a hard FK into them (or vice versa).
ALTER TABLE payments ADD COLUMN voucher_id TEXT;
-- Mirror onto payments_archive, per 040_reset_archive.sql's own header rule
-- (same as 069 did for sales.voucher_issue_total): without this, "Clear
-- transaction history" would drop which voucher a reset-archived redemption
-- debited on archive and RestoreResetBatch could never bring it back.
ALTER TABLE payments_archive ADD COLUMN voucher_id TEXT;
