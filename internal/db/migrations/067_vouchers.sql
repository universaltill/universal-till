-- 067: vouchers as a 0% liability class (ut-docs#1008). A multi-purpose
-- voucher's issue is NOT revenue and NOT a taxable supply — VAT arises only
-- at redemption, against the redeemed goods' own rates. So a voucher is
-- deliberately NOT a sale_lines row (sale_lines requires item_id/variant_id
-- by CHECK constraint, and a fake catalog item would put the issue amount
-- back into article revenue — the exact bug this card exists to avoid);
-- it is a sale-level financial event, same precedent as sale_charges
-- (ADR-0062).
--
-- issued_sale_id / voucher_transactions.sale_id are INFORMATIONAL soft
-- references with NO FK to sales(id), deliberately: ResetTransactionHistory
-- (ADR-0042, reset_archive_repo.go) archives-and-deletes the sales row
-- while a voucher issued in that sale can still be outstanding weeks later.
-- The liability must survive a transaction-history reset, so these tables
-- are NOT in resetArchiveTables and must never gain a hard FK to any table
-- that is — same reasoning as worker_allocations.source_id (ADR-0063).
--
-- Only 'multi_purpose' is a valid voucher_type in this card: a
-- single-purpose voucher (VAT at issue) is a separate follow-up
-- (ut-docs#1037) and will extend the CHECK in its own migration.
CREATE TABLE IF NOT EXISTS vouchers (
    id                TEXT PRIMARY KEY,             -- stable voucher identifier/code
    holder_label      TEXT,                         -- optional, e.g. customer name
    original_amount   INTEGER NOT NULL,             -- minor units, as issued
    balance           INTEGER NOT NULL,             -- minor units, outstanding liability
    currency          TEXT NOT NULL DEFAULT 'EUR',
    voucher_type      TEXT NOT NULL DEFAULT 'multi_purpose'
                          CHECK (voucher_type IN ('multi_purpose')),
    status            TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','redeemed','void')),
    issued_sale_id    TEXT,                         -- informational only, NO FK (see header)
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS voucher_transactions (
    id           TEXT PRIMARY KEY,
    voucher_id   TEXT NOT NULL REFERENCES vouchers (id),
    sale_id      TEXT,                              -- informational only, NO FK (see header)
    type         TEXT NOT NULL CHECK (type IN ('issue','redemption')),
    amount       INTEGER NOT NULL,                  -- minor units, positive
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_voucher_tx_voucher ON voucher_transactions (voucher_id);
