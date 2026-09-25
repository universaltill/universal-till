-- ut-docs#2501: a one-row generation counter for the cashier sell screen's
-- in-memory tile cache (internal/ui's SellScreenCache). The cache keys every
-- rendered /ui/buttons and /ui/buttons/category fragment on
-- sync_admin_version.generation (023 — bumped by every admin table: items,
-- categories, shortcut_buttons, settings, modifiers, variants, barcodes, …)
-- AND on this counter, read together in ONE query per request
-- (internal/data's SellScreenRepo.SellScreenVersion). This counter covers
-- the two tables the tiles render from that 023 does not:
--
--   * price_history — never synced (ADR-0099), so not an adminTables entry;
--     a price override / scheduled price written here changes a tile's
--     price.
--   * item_images — item photos, not synced (the D2 limit); a new or
--     replaced thumbnail changes a tile's image.
--
-- A price_history row whose starts_at/ends_at is still in the future changes
-- the tiles later WITHOUT any write; the cache handles that by expiring an
-- entry at the next such boundary (SellScreenRepo.NextPriceBoundary), not
-- through this counter.
--
-- Same shape as 023: one AFTER INSERT / UPDATE / DELETE trigger per table
-- (SQLite takes one event per CREATE TRIGGER), IF NOT EXISTS / INSERT OR
-- IGNORE so the file stays re-appliable (023's header explains why).
-- sell_screen_version itself is sync-internal and per-database — classified
-- in sync_admin_repo.go's nonAdminTables, never synced.

CREATE TABLE IF NOT EXISTS sell_screen_version (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO sell_screen_version (id, generation) VALUES (1, 0);

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_price_history_ins AFTER INSERT ON price_history
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_price_history_upd AFTER UPDATE ON price_history
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_price_history_del AFTER DELETE ON price_history
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_images_ins AFTER INSERT ON item_images
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_images_upd AFTER UPDATE ON item_images
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_images_del AFTER DELETE ON item_images
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;
