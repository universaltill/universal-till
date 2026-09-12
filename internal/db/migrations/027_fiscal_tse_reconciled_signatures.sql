-- 027_fiscal_tse_reconciled_signatures.sql — ADR-0077 Decision 3/4 (ut-docs#1520).
--
-- §6 KassenSichV TSE evidence a fiscal.sign.reconcile.ask answer confirmed
-- for a sale that completed UNSIGNED at tender time (an unsigned_fiscal_signing
-- gap whose outcome was a backend-level failure — budget expired / backend
-- declared unreachable) and whose signature fiskaly's own service had in
-- fact already produced. Same column set as fiscal_tse_signatures (the
-- tender-time evidence table) plus the two facts the reconcile check itself
-- established: tx_id (the fiscal.sign.start identifier the answer was
-- matched against, '' on the degraded window-only tier) and check_tier
-- ('exact' = tx_id equality, 'window' = start/log time within a bounded
-- window of the sale's failed_at — the honestly-named weaker tier ADR-0077
-- D3/D5 wants the tax-advisor go-live review to see called out as degraded).
--
-- DELIBERATELY A SEPARATE TABLE, not rows in fiscal_tse_signatures: both
-- receipt render paths (the inline HTML receipt in internal/pages/pos_api.go
-- and the ESC/POS reprint in internal/pages/print_api.go's buildReceiptDoc)
-- read fiscal_tse_signatures by sale_id and render whatever they find as
-- the receipt's TSE block. ADR-0077 D4 is absolute: a reconcile outcome
-- never changes what any receipt render shows, original or reprint — the
-- notice printed at tender time is permanent, and fiskaly's own guidance is
-- that retrieving a signature later "does not mean altering or reprinting
-- the original receipt". Keeping reconciled evidence out of the table the
-- receipts read makes that guarantee structural, not a per-render check
-- that a later edit could forget. This table is read by the audit trail /
-- a future DSFinV-K export only — never by a receipt.
--
-- Idempotent by primary key (INSERT ... ON CONFLICT DO NOTHING): a sale is
-- reconciled at most once; a repeated sweep pass writes nothing new.
--
-- Shipped as an ADDITIVE migration rather than an edit to 001_init.sql:
-- internal/db/db.go's verifyAppliedMigrations hard-fails an already-migrated
-- database on a checksum drift of 001 — see 005_fiscal_sign_starts.sql.
-- IF NOT EXISTS, same as every CREATE TABLE migration after 009: the
-- migration-replay tests (internal/db's openAtPreMigrationSchema helpers)
-- rewind schema_migrations on a fully-built schema and re-run everything
-- above the rewound version, so a bare CREATE TABLE here would fail them.
CREATE TABLE IF NOT EXISTS fiscal_tse_reconciled_signatures (
    sale_id             TEXT PRIMARY KEY,
    tx_id               TEXT NOT NULL DEFAULT '',
    check_tier          TEXT NOT NULL,
    transaction_number  INTEGER NOT NULL DEFAULT 0,
    signature_counter   INTEGER NOT NULL DEFAULT 0,
    serial_number       TEXT NOT NULL DEFAULT '',
    start_time          TEXT NOT NULL DEFAULT '',
    log_time            TEXT NOT NULL DEFAULT '',
    signature           TEXT NOT NULL,
    signature_algorithm TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);
