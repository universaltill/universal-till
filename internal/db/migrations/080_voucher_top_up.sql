-- 080_voucher_top_up.sql — ut-docs#4252: a customer can add value to an
-- existing multi-purpose gift voucher as a sale (a "top-up"). The till
-- credits the existing voucher in the sale's own transaction and records
-- the event as a ledger row of a fourth type, 'top_up', carrying the sale
-- id and the (positive) amount added — so voucher_transactions has to admit
-- it:
--
--   type  CHECK widened to ('issue','redemption','adjustment','top_up').
--
-- Same recipe, same safety argument as 074_voucher_adjustments.sql (whose
-- header explains it in full): SQLite has no ALTER for a CHECK constraint,
-- and voucher_transactions is a LEAF table — it references vouchers(id) and
-- nothing references it (no REFERENCES voucher_transactions, no trigger or
-- view names it) — so create the replacement, copy by column name, drop,
-- rename, recreate the indexes. 074 is the last migration that touched the
-- table: its column list (001's six plus actor_user_id/reason) and its two
-- indexes are recreated verbatim below.
CREATE TABLE voucher_transactions_new (
    id            TEXT PRIMARY KEY,
    voucher_id    TEXT NOT NULL REFERENCES vouchers (id),
    sale_id       TEXT,
    type          TEXT NOT NULL CHECK (type IN ('issue','redemption','adjustment','top_up')),
    amount        INTEGER NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    actor_user_id TEXT,
    reason        TEXT
);

INSERT INTO voucher_transactions_new (id, voucher_id, sale_id, type, amount, created_at, actor_user_id, reason)
SELECT id, voucher_id, sale_id, type, amount, created_at, actor_user_id, reason
FROM voucher_transactions;

DROP TABLE voucher_transactions;

ALTER TABLE voucher_transactions_new RENAME TO voucher_transactions;

CREATE INDEX idx_voucher_tx_voucher ON voucher_transactions (voucher_id);

CREATE UNIQUE INDEX ux_voucher_tx_redemption_once
  ON voucher_transactions (voucher_id, sale_id)
  WHERE type = 'redemption';
