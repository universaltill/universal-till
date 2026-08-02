-- Stock locations gain a soft-disable flag (universaltill/ut-docs#49),
-- mirroring the pattern already used on `registers`. Existing locations
-- default to active so today's single "Main" location keeps working.
ALTER TABLE stock_locations ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1;
