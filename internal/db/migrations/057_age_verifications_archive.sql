-- 057_age_verifications_archive.sql — universaltill/ut-docs#3340 review
-- fix (blocker): the reset-archive twin of age_verifications
-- (056_age_verifications.sql).
--
-- Why: age_verifications.sale_id is a FOREIGN KEY to sales(id) with no ON
-- DELETE action (deliberately — 056's own header: a verification is a
-- historical record and must not silently vanish with its sale). This
-- project runs with foreign_keys=ON (internal/db/db.go), so once any
-- verification row existed, POSRepo.ResetTransactionHistory's
-- `DELETE FROM sales` failed outright with "FOREIGN KEY constraint failed"
-- and the whole reset rolled back — the exact trap sale_charges hit before
-- ADR-0062 added it to the archive (see resetArchiveTables' doc comment,
-- internal/data/reset_archive_repo.go).
--
-- Fix, following the established mechanism rather than inventing one
-- (ADR-0042): age_verifications joins resetArchiveTables, archived/cleared
-- BEFORE sales (child before parent) and restored AFTER it (parent before
-- child). This is the archive twin that needs.
--
-- Shape: the exact convention every *_archive table uses (001_init.sql's
-- "Reset archives" header; most recently yuzde_usulu_pool_collections_archive
-- in 037): column-identical to the live table, plus reset_batch_id; no
-- PRIMARY KEY/UNIQUE (an archive holds rows from many batches); no FKs to
-- live tables (sale_id/item_id/cashier_id only make sense within the batch
-- or are restored against live rows by RestoreResetBatch, which turns a
-- removed reference into ErrArchiveReferencesRemoved); NOT NULL and the
-- single-table outcome CHECK are kept. reset_archive_repo.go's
-- resetArchiveTables carries the matching column list.
--
-- CREATE TABLE/INDEX IF NOT EXISTS throughout, same replay-safety
-- convention as every migration since 021.
CREATE TABLE IF NOT EXISTS age_verifications_archive (
    id             TEXT NOT NULL,
    sale_id        TEXT,
    item_id        TEXT,
    item_name      TEXT NOT NULL,
    outcome        TEXT NOT NULL CHECK (outcome IN ('accepted', 'refused')),
    cashier_id     TEXT,
    created_at     TEXT NOT NULL,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_age_verifications_archive_batch
    ON age_verifications_archive (reset_batch_id);
