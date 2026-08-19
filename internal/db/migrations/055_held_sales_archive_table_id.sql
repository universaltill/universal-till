-- ut-docs#814 (ADR-0054) added held_sales.table_id (054_tables.sql). Reviewer
-- finding (2026-08-19 code review): 040_reset_archive.sql's own documented
-- invariant is that every *_archive twin stays column-identical to its live
-- table across every later ALTER — held_sales_archive was left behind.
-- Harmless today (nothing writes table_id until ut-docs#820 ships), but a
-- reset→restore after #820 lands would otherwise silently drop every parked
-- order's table assignment. Nullable, no default: purely additive, safe on
-- existing archive rows.
ALTER TABLE held_sales_archive ADD COLUMN table_id TEXT REFERENCES tables(id);
