-- 036_voucher_single_purpose_type.sql — ADR-0105 (ut-docs#1037): single-
-- purpose vouchers (§3 Abs. 15 UStG) are taxed AT ISSUE, so the vouchers
-- table needs two things 001_init.sql deliberately left for this card:
--   * vouchers.voucher_type's CHECK widens from ('multi_purpose') to
--     ('multi_purpose', 'single_purpose');
--   * a new nullable vouchers.tax_rate_bp — the basis-point rate a
--     single-purpose voucher was taxed at when sold, stamped immutably at
--     issue so redemption-time validation and reporting can recover it
--     without re-reading a settings row that may since have changed. NULL
--     for every multi-purpose voucher (no rate is fixed until redemption).
--
-- Why a rebuild (create replacement, copy, drop, rename): SQLite has no
-- ALTER TABLE for a CHECK constraint, and every file under this directory
-- is frozen the moment it merges (ADR-0100, shipped_migrations_test.go) —
-- so 001 cannot be edited. 018 (ADR-0088) is the established rebuild
-- pattern for a LEAF table; 034 (ADR-0101) proved the ordering for a
-- PARENT. vouchers is a PARENT: voucher_transactions.voucher_id
-- REFERENCES vouchers (id), no ON DELETE clause (so the default NO ACTION —
-- but see below for why the ordering still matters).
--
-- ORDER MATTERS — this is the whole safety argument (034's header is the
-- required reading): internal/db/db.go pins PRAGMA foreign_keys = ON on
-- every connection, and applyMigration runs this file inside one
-- transaction. 003_kitchen_station_display_flag.sql proved that DROP TABLE
-- on an FK PARENT whose children still reference it performs an implicit
-- DELETE FROM that fires every child's ON DELETE action for real — and for
-- a NO ACTION child it raises a deferred FK violation that aborts the whole
-- migration at commit instead. Either way, never drop a referenced parent
-- while children point at it. So:
--
--   0. ALTER TABLE vouchers ADD COLUMN tax_rate_bp — on the OLD table,
--      FIRST. This looks redundant (the replacement table declares the
--      column anyway) but it is what makes the file replay-safe WITHOUT
--      data loss: openAtPreMigrationSchema's replay contract (every
--      migration here must be re-runnable against an already-migrated
--      database) means step 1's copy runs a second time against a vouchers
--      table that already has single-purpose rows carrying a rate. 034
--      handled replay by naming only SURVIVING columns, which works when a
--      column is being REMOVED; here one is being ADDED, so the copy must
--      name tax_rate_bp — and on a first run that column does not exist
--      yet. The runner skips an ADD COLUMN whose column already exists
--      (execMigrationStatements, ut-docs#1412), so on the first run this
--      adds the column (all NULL) and on a replay it is a no-op; the copy
--      below can then name tax_rate_bp unconditionally in both cases.
--   1. Create vouchers_new (001's shape + the widened CHECK + tax_rate_bp)
--      and copy every row in, tax_rate_bp included.
--   2. Rebuild the one child, voucher_transactions, the 018 way: identical
--      columns/PK/CHECK except `REFERENCES vouchers_new (id)`. It is a
--      LEAF (grep `REFERENCES voucher_transactions` in this directory finds
--      nothing; payments.voucher_id is a plain TEXT column with no FK), so
--      dropping the old child cascades into nothing and violates nothing.
--      Recreate its two indexes — 001's idx_voucher_tx_voucher and 012's
--      ux_voucher_tx_redemption_once partial unique index (ADR-0084's
--      redemption idempotency key) — DROP TABLE took both with it.
--   3. DROP TABLE vouchers — now CHILDLESS (the only referencing table
--      points at vouchers_new), so its implicit DELETE FROM touches nothing.
--   4. ALTER TABLE vouchers_new RENAME TO vouchers. SQLite >= 3.26 with
--      legacy_alter_table OFF (the default; modernc.org/sqlite ships 3.4x)
--      rewrites the child's `REFERENCES vouchers_new` to the new name as
--      part of the rename — the same behaviour 018 and 034 rely on.
--
-- Neither table carries triggers (vouchers/voucher_transactions are not in
-- sync_admin_repo.go's adminTables — a voucher is a per-shop liability
-- ledger, not catalog), so nothing else needs recreating.
--
-- Pinned by migration_036_voucher_single_purpose_type_test.go: a real
-- pre-036 database (001..035 through the real runner, seeded with vouchers
-- and their issue/redemption/import rows) survives the upgrade byte-for-
-- byte with every pre-existing voucher's tax_rate_bp NULL, the CHECK admits
-- single_purpose and rejects anything else, the FK points at vouchers, both
-- indexes exist and the partial unique one still enforces, PRAGMA
-- foreign_key_check is clean, and a single-purpose row written AFTER the
-- upgrade keeps its rate across a replay.

-- ---------------------------------------------------------------------
-- 0. The new column on the old table (skipped by the runner on replay).
-- ---------------------------------------------------------------------
ALTER TABLE vouchers ADD COLUMN tax_rate_bp INTEGER;

-- ---------------------------------------------------------------------
-- 1. The widened parent, and the voucher rows.
-- ---------------------------------------------------------------------
CREATE TABLE vouchers_new (
    id                TEXT PRIMARY KEY,
    holder_label      TEXT,
    original_amount   INTEGER NOT NULL,
    balance           INTEGER NOT NULL,
    currency          TEXT NOT NULL DEFAULT 'EUR',
    voucher_type      TEXT NOT NULL DEFAULT 'multi_purpose'
                          CHECK (voucher_type IN ('multi_purpose', 'single_purpose')),
    status            TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','redeemed','void')),
    issued_sale_id    TEXT,
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    tax_rate_bp       INTEGER
);

INSERT INTO vouchers_new (id, holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id, created_at, tax_rate_bp)
SELECT id, holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id, created_at, tax_rate_bp
FROM vouchers;

-- ---------------------------------------------------------------------
-- 2. voucher_transactions (001_init.sql shape), re-pointed at the new parent.
-- ---------------------------------------------------------------------
CREATE TABLE voucher_transactions_new (
    id           TEXT PRIMARY KEY,
    voucher_id   TEXT NOT NULL REFERENCES vouchers_new (id),
    sale_id      TEXT,
    type         TEXT NOT NULL CHECK (type IN ('issue','redemption')),
    amount       INTEGER NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO voucher_transactions_new (id, voucher_id, sale_id, type, amount, created_at)
SELECT id, voucher_id, sale_id, type, amount, created_at
FROM voucher_transactions;

DROP TABLE voucher_transactions;
ALTER TABLE voucher_transactions_new RENAME TO voucher_transactions;

CREATE INDEX IF NOT EXISTS idx_voucher_tx_voucher ON voucher_transactions (voucher_id);

CREATE UNIQUE INDEX IF NOT EXISTS ux_voucher_tx_redemption_once
  ON voucher_transactions (voucher_id, sale_id)
  WHERE type = 'redemption';

-- ---------------------------------------------------------------------
-- 3 + 4. The old parent is childless now: drop it, rename the replacement
-- (the rename rewrites the child's REFERENCES clause).
-- ---------------------------------------------------------------------
DROP TABLE vouchers;
ALTER TABLE vouchers_new RENAME TO vouchers;
