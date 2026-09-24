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

-- Modifier groups are shop-wide (ADR-0101, migration 034): the item delete
-- below cascades only the demo items' own item_modifier_group_links and
-- item_modifier_group_opt_outs rows. A group a demo item used is never
-- deleted with it — it stays, unassigned if that was its only item.

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

-- The demo tax code (ut-docs#167) goes only when no remaining item — a
-- kept demo item or an operator's own — uses it (items.tax_code_id is a
-- live FK), and only while still pristine. The pristine rule is NOT
-- relaxed in the relaxed variant: a changed rate is shop tax configuration,
-- not a sample row. A leftover takeaway_rate_overrides entry in the German
-- tax plugin's settings for a removed code is inert (it keys a code no
-- item has) and shows as an orphan row in that plugin's editor.
-- A single-item "Remove anyway" (DemoSeedRepo.RemoveDemoItem) does not
-- sweep it, same as it leaves demo categories/brands; the next bulk
-- removal does.
DELETE FROM tax_codes
 WHERE EXISTS (SELECT 1 FROM demo_seed_tax_codes d
               WHERE d.id = tax_codes.id
                 AND d.name = tax_codes.name
                 AND d.rate_basis_points = tax_codes.rate_basis_points
                 AND d.takeaway_rate_basis_points IS tax_codes.takeaway_rate_basis_points)
   AND NOT EXISTS (SELECT 1 FROM items i WHERE i.tax_code_id = tax_codes.id);

-- TEMP tables are per-connection: drop them so a later run on the same
-- pooled connection starts clean.
DROP TABLE IF EXISTS temp.demo_seed_removable;
DROP TABLE IF EXISTS temp.demo_seed_items;
DROP TABLE IF EXISTS temp.demo_seed_categories;
DROP TABLE IF EXISTS temp.demo_seed_brands;
DROP TABLE IF EXISTS temp.demo_seed_tax_codes;
