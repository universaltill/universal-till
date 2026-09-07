-- 005_fiscal_sign_starts.sql — ADR-0077 Decision 1 (ut-docs#1519).
--
-- Best-effort persistence for the fiscal.sign.start round trip: when a
-- subscribed signer's start handler answers with {"status":"acknowledged",
-- "tx_id":"…","tx_revision":1} before the background dispatch goroutine is
-- abandoned, core records the identifier here, keyed by sale_id, so the
-- later fiscal.sign.ask ("finish") dispatch for the SAME sale can echo it
-- back as started_tx_id/started_tx_revision (ADR-0077 D2). Absence of a row
-- is the common, honest degraded case (the round trip never completed in
-- time) — never an error, never backfilled.
--
-- Shipped as an ADDITIVE migration rather than an edit to 001_init.sql:
-- editing 001_init.sql changes its checksum, and internal/db/db.go's
-- verifyAppliedMigrations hard-fails an already-migrated database on any
-- checksum drift (idempotentRerunVersions is empty; version 1 is not
-- allowlisted) — bricking every device that already migrated, including the
-- pilot install. 002_refund_of_line_id.sql, 003_kitchen_station_display_flag.sql
-- and 004_brands_is_active.sql all document this same trap.
CREATE TABLE fiscal_sign_starts (
    sale_id      TEXT PRIMARY KEY,
    tx_id        TEXT NOT NULL,
    tx_revision  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
