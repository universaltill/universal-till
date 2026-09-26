-- ut-docs#2765: sell_screen_version.generation (042) becomes the open sale
-- screen's catalog-only live-refresh signal as well as half of the tile
-- cache's key. GET /ui/buttons/version serves it (one single-row read) and
-- web/public/sell-screen-watch.js polls it while the sale screen is
-- visible: when it differs from the version the grid was rendered at
-- (/ui/buttons' X-UT-Sell-Version header) the grid refetches itself. So a
-- catalog change made OUTSIDE this document — another till or tab, a
-- my./cloud catalog push, a main-till -> replica admin sync pull — reaches
-- an already-open sale screen within a few seconds, with no tap or reload.
--
-- 042 covered price_history and item_images. This file adds the same
-- AFTER INSERT / UPDATE / DELETE bump triggers to every OTHER table the
-- /ui/buttons and /ui/buttons/category renders read (internal/ui/buttons.go
-- loadAllActive / loadWith / LoadCategories / LoadCategoriesForAdmin and
-- the repo queries they call):
--
--   * items                          — ListItems, SellScreenStates,
--                                      ItemCurrentPrices, LoadGridButtons,
--                                      ListCategoriesForAdmin (item counts)
--   * categories                     — ListActiveCategories,
--                                      ListCategoriesForAdmin
--   * shortcut_buttons               — LoadGridButtons
--   * item_barcodes                  — ItemBarcodes (a tile's scan code)
--   * item_variants, variant_barcodes — ItemIDsWithVariants (picker badge)
--   * item_modifier_groups, item_modifier_group_links,
--     category_modifier_group_links, item_modifier_group_opt_outs
--                                    — ItemIDsWithModifiers
--   * translation_overrides          — the grid's own T-keyed chrome text
--
-- Deliberately NOT covered, so a completed sale never moves the counter:
-- sales / sale_lines / payments / inventory / stock_movements / audit_log /
-- tills / plugin_storage — and settings. The grid does read two settings
-- (the enabled barcode symbologies behind a tile's scan code, and
-- sale.browsing_mode / currency via the process's runtime state), but
-- settings also holds per-till and operational state written by ordinary
-- till activity, so bumping on it would refresh every open grid for
-- unrelated writes; those two are rare shop-wide
-- configuration changes and stay covered by the tile cache's
-- sync_admin_version key (023) and the grid's own page load. The set is
-- pinned by internal/data's TestSellScreenVersion_TriggersExactlyOnGridTables.
--
-- Nothing here changes the tile cache's correctness: every table below is
-- already an admin table, so 023's sync_admin_version half of the key moved
-- on these writes before; the sell half now moves with it.
--
-- Same shape as 023/042: one trigger per table and event (SQLite takes one
-- event per CREATE TRIGGER), IF NOT EXISTS so the file stays re-appliable
-- (023's header explains why).

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_items_ins AFTER INSERT ON items
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_items_upd AFTER UPDATE ON items
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_items_del AFTER DELETE ON items
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_categories_ins AFTER INSERT ON categories
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_categories_upd AFTER UPDATE ON categories
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_categories_del AFTER DELETE ON categories
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_shortcut_buttons_ins AFTER INSERT ON shortcut_buttons
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_shortcut_buttons_upd AFTER UPDATE ON shortcut_buttons
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_shortcut_buttons_del AFTER DELETE ON shortcut_buttons
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_barcodes_ins AFTER INSERT ON item_barcodes
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_barcodes_upd AFTER UPDATE ON item_barcodes
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_barcodes_del AFTER DELETE ON item_barcodes
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_variants_ins AFTER INSERT ON item_variants
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_variants_upd AFTER UPDATE ON item_variants
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_variants_del AFTER DELETE ON item_variants
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_variant_barcodes_ins AFTER INSERT ON variant_barcodes
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_variant_barcodes_upd AFTER UPDATE ON variant_barcodes
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_variant_barcodes_del AFTER DELETE ON variant_barcodes
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_groups_ins AFTER INSERT ON item_modifier_groups
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_groups_upd AFTER UPDATE ON item_modifier_groups
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_groups_del AFTER DELETE ON item_modifier_groups
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_group_links_ins AFTER INSERT ON item_modifier_group_links
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_group_links_upd AFTER UPDATE ON item_modifier_group_links
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_group_links_del AFTER DELETE ON item_modifier_group_links
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_category_modifier_group_links_ins AFTER INSERT ON category_modifier_group_links
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_category_modifier_group_links_upd AFTER UPDATE ON category_modifier_group_links
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_category_modifier_group_links_del AFTER DELETE ON category_modifier_group_links
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_group_opt_outs_ins AFTER INSERT ON item_modifier_group_opt_outs
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_group_opt_outs_upd AFTER UPDATE ON item_modifier_group_opt_outs
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_item_modifier_group_opt_outs_del AFTER DELETE ON item_modifier_group_opt_outs
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_translation_overrides_ins AFTER INSERT ON translation_overrides
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_translation_overrides_upd AFTER UPDATE ON translation_overrides
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sell_screen_version_translation_overrides_del AFTER DELETE ON translation_overrides
BEGIN
  UPDATE sell_screen_version SET generation = generation + 1 WHERE id = 1;
END;
