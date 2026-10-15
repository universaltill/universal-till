-- 074_voucher_adjustments.sql — ut-docs#4253: a manager can correct a gift
-- voucher's balance with a reason (no payment moves). The correction is a
-- ledger event like issue and redemption, so voucher_transactions has to
-- admit a third type, 'adjustment', and record who made it and why:
--
--   type           CHECK widened to ('issue','redemption','adjustment').
--                  An adjustment's amount is SIGNED (new balance − old
--                  balance); issue/redemption rows stay positive as before.
--   actor_user_id  The manager who made the correction (NULL for every
--                  issue/redemption row, which carry no actor today).
--   reason         The operator-typed reason (NULL for issue/redemption).
--
-- Why a rebuild and not an edit of 001_init.sql: verifyAppliedMigrations
-- hard-fails every already-migrated device on a checksum drift of an
-- applied file (ADR-0100), and SQLite has no ALTER for a CHECK constraint,
-- so widening one is the documented rebuild 018_plugin_entries_type_layout
-- uses: create the replacement, copy by column name, drop, rename,
-- recreate indexes.
--
-- Why it is SAFE here, unlike `vouchers` (036 refused to rebuild that one):
-- vouchers is a PARENT — voucher_transactions.voucher_id references it — so
-- dropping it would orphan or cascade the ledger. voucher_transactions is a
-- LEAF: it references vouchers(id) and NOTHING references it (grep
-- "REFERENCES voucher_transactions" across migrations finds no row, and no
-- trigger or view names the table). Dropping child rows never violates a
-- constraint, and the RENAME has no REFERENCES clauses elsewhere to
-- rewrite. Migrations 002–073 touched the table only through 012's partial
-- unique index, recreated verbatim below with 001's idx_voucher_tx_voucher;
-- no later migration added a column, so 001's column list is complete.
CREATE TABLE voucher_transactions_new (
    id            TEXT PRIMARY KEY,
    voucher_id    TEXT NOT NULL REFERENCES vouchers (id),
    sale_id       TEXT,
    type          TEXT NOT NULL CHECK (type IN ('issue','redemption','adjustment')),
    amount        INTEGER NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    actor_user_id TEXT,
    reason        TEXT
);

INSERT INTO voucher_transactions_new (id, voucher_id, sale_id, type, amount, created_at)
SELECT id, voucher_id, sale_id, type, amount, created_at
FROM voucher_transactions;

DROP TABLE voucher_transactions;

ALTER TABLE voucher_transactions_new RENAME TO voucher_transactions;

CREATE INDEX idx_voucher_tx_voucher ON voucher_transactions (voucher_id);

CREATE UNIQUE INDEX ux_voucher_tx_redemption_once
  ON voucher_transactions (voucher_id, sale_id)
  WHERE type = 'redemption';
