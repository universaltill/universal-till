-- Demo catalogue removal, RELAXED variant (ut-docs#1840). Same script as
-- remove_demo.sql (keep both in sync deliberately — see that file's own
-- header for the full explanation of every safety clause below, none of
-- which changed here) with exactly ONE difference: the "untouched"
-- predicate drops the PRISTINE match (sku/name/base_price still equal to
-- the seeded values). DemoSeedRepo.RemoveDemoCatalogue picks this variant
-- instead of the strict one only when the till as a whole has no
-- non-sample trading history (demoTillHasNoRealHistorySQL) — a shop that
-- has never traded for real has nothing for the pristine rule to protect,
-- so a merchant who renamed/repriced/re-SKU'd a demo item while trying the
-- till out (ut-docs#1840: "I ticked the wrong box") can still remove it in
-- one tap. The moment the till has ANY real (non-sample) sale_lines/
-- stock_movements row, live or archived, RemoveDemoCatalogue falls back to
-- the strict variant and an edited demo item is kept exactly as before —
-- this file's relaxation never reaches a till that has actually traded.
--
-- Every trading-history/held-basket safety clause is IDENTICAL to
-- remove_demo.sql, unrelaxed: a demo item that has itself been sold,
-- stock-adjusted, or parked in a basket (live or archived, directly or via
-- a variant) is still kept regardless of mode — relaxing those would risk
-- an FK failure or a silently orphaned archive reference, which is exactly
-- what ut-docs#1840's own "Do not regress" section forbids widening.
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
                  WHERE v.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM sale_lines_archive sl WHERE sl.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM sale_lines_archive sl
                  JOIN item_variants v ON v.id = sl.variant_id
                  WHERE v.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM stock_movements_archive sm WHERE sm.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM stock_movements_archive sm
                  JOIN item_variants v ON v.id = sm.variant_id
                  WHERE v.item_id = i.id)
  AND NOT EXISTS (SELECT 1 FROM held_sales h
                  WHERE h.payload LIKE '%"item_id":"' || i.id || '"%')
  AND NOT EXISTS (SELECT 1 FROM held_sales h
                  JOIN item_variants v ON v.item_id = i.id
                  WHERE h.payload LIKE '%"variant_id":"' || v.id || '"%')
  AND NOT EXISTS (SELECT 1 FROM held_sales_archive h
                  WHERE h.payload LIKE '%"item_id":"' || i.id || '"%')
  AND NOT EXISTS (SELECT 1 FROM held_sales_archive h
                  JOIN item_variants v ON v.item_id = i.id
                  WHERE h.payload LIKE '%"variant_id":"' || v.id || '"%');

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

-- ADR-0090 §2 (ut-docs#2013): re-anchor a modifier group shared with a
-- surviving item before the delete below can cascade it away — identical
-- to remove_demo.sql's own step (see its comment there; keep in sync).
UPDATE item_modifier_groups
   SET item_id = (SELECT l.item_id FROM item_modifier_group_links l
                   WHERE l.group_id = item_modifier_groups.id
                     AND l.item_id NOT IN (SELECT id FROM demo_seed_removable)
                   LIMIT 1)
 WHERE item_id IN (SELECT id FROM demo_seed_removable)
   AND EXISTS (SELECT 1 FROM item_modifier_group_links l
                WHERE l.group_id = item_modifier_groups.id
                  AND l.item_id NOT IN (SELECT id FROM demo_seed_removable));

-- The item delete cascades to item_barcodes, item_images, item_variants
-- (-> variant_barcodes), shortcut_buttons, related_items, item_modifiers
-- (+ item_modifier_group_links) and item_station_routes (all declared ON
-- DELETE CASCADE).
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
