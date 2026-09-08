-- ut-docs#1208 / PR #750: evidence of what Turkey's YN ÖKC fiscal device
-- printed for a sale. The device itself is the legal signer (Law No. 3100),
-- so this row is the till's record of the receipt the DEVICE issued — its
-- number, serial and Z counter — kept so a thermal copy and the device's own
-- mali fiş can be matched afterwards. Keyed 1:1 on sale_id, exactly like
-- fiscal_tse_signatures does for Germany, and classified alongside it as
-- per-till in internal/data/sync_admin_repo.go's nonAdminTables.
--
-- Deliberately a NUMBERED migration rather than an edit to 001_init.sql,
-- even though CLAUDE.md still permits editing the baseline freely
-- pre-revenue (ADR-0074). Permission is not the constraint here — the
-- MECHANISM is: internal/db compares each applied migration's checksum on
-- every boot, and `idempotentRerunVersions` is empty, so a till that has
-- already run 001 hits the hard "a migration file was renamed or edited
-- after being applied; delete the data directory and start again" error and
-- refuses to open its database. Every existing install — the test tablet
-- and Pi included, with their real catalogs — would have had to be wiped to
-- take this feature. As migration 010 they simply upgrade.
CREATE TABLE IF NOT EXISTS fiscal_device_receipts (
    sale_id      TEXT PRIMARY KEY,
    device_kind  TEXT NOT NULL DEFAULT 'okc',
    maker        TEXT NOT NULL DEFAULT '',
    serial       TEXT NOT NULL DEFAULT '',
    receipt_no   TEXT NOT NULL,
    receipt_kind TEXT NOT NULL DEFAULT 'mali_fis',
    z_no         INTEGER NOT NULL DEFAULT 0,
    issued_at    TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_fiscal_device_receipts_created ON fiscal_device_receipts (created_at);
