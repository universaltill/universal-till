-- 002_refund_of_line_id.sql — ut-docs#1560.
--
-- Adds refund_of_line_id to sale_lines (and its archive twin): for a
-- RETURN sale's line, the specific ORIGINAL sale_lines.id it was refunded
-- against; NULL for an original (non-return) line, and for any return line
-- written before this column existed.
--
-- RefundLineKey (item/variant/price/mode) pools sibling original lines
-- together for the QUANTITY double-refund guard deliberately (a customer
-- returning "2 units of item X" doesn't care which physical scan they came
-- from) -- but that same pooling silently blends DISCOUNT together too
-- when sibling lines carry different line_discount amounts, which is
-- wrong (see internal/data/pos_repo.go's ReturnedLineDiscountsByOriginalLine
-- doc comment). This column lets the per-line discount clamp track
-- cumulative refunded discount against the one original line a
-- non-uniform-key return line actually came from, instead of the whole
-- key's fungible pool.
--
-- Shipped as an ADDITIVE migration rather than an edit to 001_init.sql
-- (which ADR-0074 Decision 1 would otherwise permit pre-first-paying-shop):
-- editing 001_init.sql changes its checksum, and internal/db/db.go's
-- verifyAppliedMigrations hard-fails an already-migrated database on any
-- checksum drift (by design -- ADR-0074's own protection against a
-- migration being silently edited out from under a device that already
-- applied it), forcing a full data-directory wipe on every existing
-- install (dev machines, the TECLAST tablet, both Pis, the shadow-café
-- pilot). A plain additive ALTER TABLE needs none of that.
--
-- No FK on sale_lines_archive.refund_of_line_id, matching the existing
-- precedent for invoices_archive.original_invoice_id (also a self-
-- reference, also FK-less on the archive side): an archived return line's
-- original line may itself have been archived (and later restored to a
-- different physical row id is never the case here, but the column is
-- cold storage, not a live constraint surface) -- see reset_archive_repo.go.
ALTER TABLE sale_lines ADD COLUMN refund_of_line_id TEXT REFERENCES sale_lines (id);

-- Index the new FK's child column: with foreign_keys=ON (internal/db/db.go),
-- SQLite scans the child table on every parent-row delete/cascade when the
-- child key column is unindexed -- every ON DELETE CASCADE from sales, and
-- ResetTransactionHistory's own DELETE FROM sale_lines, would otherwise be
-- an unindexed scan per deleted row (independent review finding on
-- ut-docs#1560). Also serves ReturnedLineDiscountsByOriginalLine/
-- ReturnedQuantitiesByOriginalLine's own GROUP BY refund_of_line_id.
CREATE INDEX idx_sale_lines_refund_of_line_id ON sale_lines (refund_of_line_id);

ALTER TABLE sale_lines_archive ADD COLUMN refund_of_line_id TEXT;
