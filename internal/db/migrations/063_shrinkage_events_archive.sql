-- 063_shrinkage_events_archive.sql — universaltill/ut-docs#3452: the
-- reset-archive twin of shrinkage_events (035_shrinkage_events.sql).
--
-- Why: "Clear transaction history" (ADR-0042) moved every transactional
-- table into its *_archive twin except shrinkage_events, which it neither
-- cleared nor archived. A void/comp/waste rung up while training before
-- go-live therefore stayed in the Shrinkage & Loss report after the reset,
-- and (since ut-docs#3394) pinned its item against Catalog cleanup and
-- Remove sample data for good. shrinkage_events now joins
-- resetArchiveTables (internal/data/reset_archive_repo.go) like
-- age_verifications did in 057.
--
-- Shape: the exact convention every *_archive table uses (001_init.sql's
-- "Reset archives" header; most recently age_verifications_archive in 057):
-- column-identical to the live table, plus reset_batch_id; no PRIMARY KEY
-- (an archive holds rows from many batches); no FKs to live tables
-- (item_id/register_id are restored against live rows by RestoreResetBatch,
-- which turns a removed reference into ErrArchiveReferencesRemoved); NOT
-- NULL, the order_type default and the reason_category CHECK are kept.
-- reset_archive_repo.go's resetArchiveTables carries the matching column
-- list.
--
-- CREATE TABLE/INDEX IF NOT EXISTS throughout, same replay-safety
-- convention as every migration since 021.
CREATE TABLE IF NOT EXISTS shrinkage_events_archive (
    id                   TEXT NOT NULL,
    reason_category      TEXT NOT NULL CHECK (reason_category IN ('void', 'comp', 'waste')),
    item_id              TEXT,
    item_name            TEXT NOT NULL,
    sku                  TEXT,
    quantity             REAL NOT NULL,
    unit_price_minor     INTEGER NOT NULL,
    extended_value_minor INTEGER NOT NULL,
    actor_id             TEXT,
    approver_id          TEXT,
    note                 TEXT,
    order_type           TEXT NOT NULL DEFAULT '',
    register_id          TEXT,
    created_at           TEXT NOT NULL,
    reset_batch_id       TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_shrinkage_events_archive_batch
    ON shrinkage_events_archive (reset_batch_id);
CREATE INDEX IF NOT EXISTS idx_shrinkage_events_archive_item
    ON shrinkage_events_archive (item_id);
