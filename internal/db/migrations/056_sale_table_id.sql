-- Order-table assignment (universaltill/ut-docs#820, ADR-0054 follow-on).
-- held_sales.table_id and held_sales_archive.table_id already exist
-- (migrations 054/055) for a PARKED order's table. This adds the same
-- column to `sales` so a COMPLETED sale's receipt/journal/kitchen ticket
-- can still show which table it was served at, mirroring how order_type
-- was added in 026_order_type.sql. Nullable, no default: purely additive.
ALTER TABLE sales ADD COLUMN table_id TEXT REFERENCES tables(id);
CREATE INDEX idx_sales_table ON sales(table_id);

-- 040_reset_archive.sql's own documented invariant is that every *_archive
-- twin stays column-identical to its live table across every later ALTER
-- (the same rule 055_held_sales_archive_table_id.sql applied to
-- held_sales_archive after a reviewer caught it missing there). Mirror it
-- onto sales_archive in the same migration this time, rather than leaving
-- it for a follow-up review to find. internal/data/reset_archive_repo.go's
-- resetArchiveTables column list for "sales" is updated alongside this.
ALTER TABLE sales_archive ADD COLUMN table_id TEXT REFERENCES tables(id);
