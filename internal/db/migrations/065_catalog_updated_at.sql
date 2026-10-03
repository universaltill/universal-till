-- 065_catalog_updated_at.sql — universaltill/ut-docs#2817: an additional
-- till's catalogue edit is written through to the main till
-- (POST /api/sync/catalog/apply, internal/pages/sync_catalog.go), and the
-- main till refuses it with a `conflict` when the record changed there since
-- the additional till's editor loaded it (optimistic, per parent record).
-- That compare needs an updated_at on every parent catalogue table. items has
-- had one since 001; this adds it to the other four the write-through
-- covers: categories, item_variants, shortcut_buttons and
-- item_modifier_groups. Children (item/variant barcodes, modifier options,
-- link rows) are saved as part of their parent's edit and get none.
--
-- Same "2006-01-02 15:04:05" UTC text shape as items.updated_at
-- (datetime('now')). The repositories (internal/data) set
-- updated_at = datetime('now') on every UPDATE of these rows, so the value
-- moves with each catalogue write; the admin-bundle pull copies the main
-- till's value verbatim (SELECT * / column intersection,
-- sync_admin_repo.go), so an additional till's copy carries the main till's
-- stamp.
--
-- SQLite cannot ADD COLUMN with a non-constant default (datetime('now') is
-- refused with "Cannot add a column with non-constant default"), so — like
-- 030_held_sales_updated_at.sql — each column lands with a constant ''
-- placeholder and the UPDATE right after backfills every existing row. The
-- `WHERE updated_at = ''` bound keeps the backfill idempotent on replay.
--
-- The INSERT paths for these tables are many (admin screens, imports, demo
-- seed, cloud directives, the admin pull), so instead of touching each one
-- an AFTER INSERT trigger stamps a new row whose updated_at is still the
-- placeholder. A row that arrives WITH a value (the admin pull copying the
-- main till's stamp) keeps it: the WHEN clause leaves it alone. The trigger
-- UPDATE also fires the sync_admin_version / sell_screen_version bump
-- triggers (023/047) once more for the same insert — harmless, both are
-- "something changed" counters.
--
-- db.go's execMigrationStatements supplies ADD COLUMN idempotence itself
-- (ut-docs#1412), and the triggers are IF NOT EXISTS, so this file is safe
-- to replay unmodified.
ALTER TABLE categories ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE categories SET updated_at = datetime('now') WHERE updated_at = '';
ALTER TABLE item_variants ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE item_variants SET updated_at = datetime('now') WHERE updated_at = '';
ALTER TABLE shortcut_buttons ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE shortcut_buttons SET updated_at = datetime('now') WHERE updated_at = '';
ALTER TABLE item_modifier_groups ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE item_modifier_groups SET updated_at = datetime('now') WHERE updated_at = '';

CREATE TRIGGER IF NOT EXISTS trg_catalog_updated_at_categories_ins AFTER INSERT ON categories
WHEN NEW.updated_at = ''
BEGIN
  UPDATE categories SET updated_at = datetime('now') WHERE rowid = NEW.rowid;
END;

CREATE TRIGGER IF NOT EXISTS trg_catalog_updated_at_item_variants_ins AFTER INSERT ON item_variants
WHEN NEW.updated_at = ''
BEGIN
  UPDATE item_variants SET updated_at = datetime('now') WHERE rowid = NEW.rowid;
END;

CREATE TRIGGER IF NOT EXISTS trg_catalog_updated_at_shortcut_buttons_ins AFTER INSERT ON shortcut_buttons
WHEN NEW.updated_at = ''
BEGIN
  UPDATE shortcut_buttons SET updated_at = datetime('now') WHERE rowid = NEW.rowid;
END;

CREATE TRIGGER IF NOT EXISTS trg_catalog_updated_at_item_modifier_groups_ins AFTER INSERT ON item_modifier_groups
WHEN NEW.updated_at = ''
BEGIN
  UPDATE item_modifier_groups SET updated_at = datetime('now') WHERE rowid = NEW.rowid;
END;
