-- ut-docs#1817: a short, customer-meaningful order number, separate from
-- receipt_no. receipt_no stays the permanent fiscal/audit identity (ADR-0042)
-- and the refund/scan target (sibling card ut-docs#1818) -- this column is
-- purely an ADDITIONAL display field, never a replacement.
--
-- Nullable, no backfill: existing sales simply have no display number, and
-- every reader of this column falls back to receipt_no when it's empty
-- (COALESCE(NULLIF(display_no, ''), receipt_no)) -- so an old row displays
-- exactly as it did before this migration, on every surface.
--
-- sales_archive gets the same column in the SAME migration (internal/data/
-- reset_archive_repo.go's own doc comment on resetArchiveTables: a live
-- table with an _archive twin must stay column-identical, or a Settings ->
-- Data -> Clear/Restore round-trip silently drops this column on restore
-- rather than erroring -- the exact bug class migrations 055/056/007 were
-- already caught adding here). internal/data/reset_archive_repo.go's
-- resetArchiveTables "sales" cols string is updated in this same change.

ALTER TABLE sales ADD COLUMN display_no TEXT;
ALTER TABLE sales_archive ADD COLUMN display_no TEXT;
