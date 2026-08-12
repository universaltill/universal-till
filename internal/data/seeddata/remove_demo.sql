-- Demo catalogue removal (ut-docs#539). Requires demo_ids.sql to have run
-- first on the same connection (it creates the temp demo_seed_* ID tables).
-- Shared verbatim by migration 036_demo_seed_opt_in.sql and
-- DemoSeedRepo.RemoveDemoCatalogue.
--
-- "Untouched" safety rule: a demo item may only be deleted when it is still
-- flagged is_sample_data = 1 AND nothing in sale_lines or stock_movements
-- references it — directly or through one of its variants (sale_lines and
-- stock_movements have no ON DELETE CASCADE, so deleting a sold/adjusted
-- item would either fail the FK or orphan real trading history). A demo
-- category/brand goes only when, after the item pass, no remaining row
-- (demo or operator-created) still references it.
DROP TABLE IF EXISTS temp.demo_seed_removable;
CREATE TEMP TABLE demo_seed_removable AS
SELECT i.id FROM items i
JOIN demo_seed_items d ON d.id = i.id
WHERE i.is_sample_data = 1
  AND NOT EXISTS (SELECT 1 FROM sale_lines sl WHERE sl.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM sale_lines sl
                  JOIN item_variants v ON v.id = sl.variant_id
                  WHERE v.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM stock_movements sm WHERE sm.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM stock_movements sm
                  JOIN item_variants v ON v.id = sm.variant_id
                  WHERE v.item_id = i.id);

-- inventory and price_history reference items/variants WITHOUT cascade, so
-- clear them explicitly (variant rows first, via the parent item).
DELETE FROM inventory
 WHERE item_id IN (SELECT id FROM demo_seed_removable)
    OR variant_id IN (SELECT v.id FROM item_variants v
                      WHERE v.item_id IN (SELECT id FROM demo_seed_removable));
DELETE FROM price_history
 WHERE item_id IN (SELECT id FROM demo_seed_removable)
    OR variant_id IN (SELECT v.id FROM item_variants v
                      WHERE v.item_id IN (SELECT id FROM demo_seed_removable));

-- The item delete cascades to item_barcodes, item_images, item_variants
-- (-> variant_barcodes), shortcut_buttons, related_items, item_modifiers
-- and item_station_routes (all declared ON DELETE CASCADE).
DELETE FROM items WHERE id IN (SELECT id FROM demo_seed_removable);

-- Categories: children before parents (self-referencing parent_id FK has no
-- cascade and single-statement delete order is unspecified). A category
-- survives if any remaining item still uses it, or if any remaining
-- category — including an operator-created one — still nests under it.
DELETE FROM categories
 WHERE id IN (SELECT id FROM demo_seed_categories)
   AND parent_id IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM items i WHERE i.category_id = categories.id)
   AND NOT EXISTS (SELECT 1 FROM categories c WHERE c.parent_id = categories.id);
DELETE FROM categories
 WHERE id IN (SELECT id FROM demo_seed_categories)
   AND parent_id IS NULL
   AND NOT EXISTS (SELECT 1 FROM items i WHERE i.category_id = categories.id)
   AND NOT EXISTS (SELECT 1 FROM categories c WHERE c.parent_id = categories.id);

DELETE FROM brands
 WHERE id IN (SELECT id FROM demo_seed_brands)
   AND NOT EXISTS (SELECT 1 FROM items i WHERE i.brand_id = brands.id);

-- TEMP tables are per-connection: drop them so a later run on the same
-- pooled connection starts clean.
DROP TABLE IF EXISTS temp.demo_seed_removable;
DROP TABLE IF EXISTS temp.demo_seed_items;
DROP TABLE IF EXISTS temp.demo_seed_categories;
DROP TABLE IF EXISTS temp.demo_seed_brands;
