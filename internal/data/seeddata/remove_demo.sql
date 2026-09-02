-- Demo catalogue removal (ut-docs#539). Requires demo_ids.sql to have run
-- first on the same connection (it creates the temp demo_seed_* ID tables).
-- Executed verbatim by DemoSeedRepo.RemoveDemoCatalogue (until the
-- ADR-0074 migration squash, ut-docs#1425, this was also shared verbatim
-- by migration 036_demo_seed_opt_in.sql, now deleted).
--
-- "Untouched" safety rule: a demo item may only be deleted when it is still
-- flagged is_sample_data = 1 AND still PRISTINE — its sku/name/base_price
-- still match the values it was seeded with (ut-docs#566: a shop that
-- renamed/repriced a demo item before ever selling or stock-adjusting it
-- has already made it real, and trading history alone missed that) — AND
-- nothing in sale_lines or stock_movements references it, LIVE OR ARCHIVED —
-- directly or through one of its variants (sale_lines and stock_movements
-- have no ON DELETE CASCADE, so deleting a sold/adjusted item would either
-- fail the FK or orphan real trading history; the *_archive twins carry no
-- FK to items at all, so deleting the item there would silently orphan the
-- archived reference instead of failing loudly) — AND no HELD (parked) sale
-- references it either, the same signal remove_demo_customers_promos.sql
-- already checks for demo customers (ut-docs#633): a held_sales row is an
-- in-progress basket that hasn't reached sale_lines/stock_movements yet, but
-- still FK-fails on tender if the item (or variant) it was parked against no
-- longer exists. A demo category/brand goes only when, after the item pass,
-- no remaining row (demo or operator-created) still references it.
--
-- The *_archive clauses (ut-docs#640) close the same gap
-- ErrArchiveReferencesRemoved documents (internal/data/reset_archive_repo.go):
-- right after a reset-transactions run, the live sale_lines/stock_movements
-- tables are empty, so the live-only clauses above see nothing — the real
-- references sit in sale_lines_archive/stock_movements_archive instead.
-- Without this, "Remove sample data" could delete a demo item a still-
-- restorable archive batch depends on, and a later RestoreResetBatch would
-- then hit a live FK it can no longer satisfy.
DROP TABLE IF EXISTS temp.demo_seed_removable;
CREATE TEMP TABLE demo_seed_removable AS
SELECT i.id FROM items i
JOIN demo_seed_items d ON d.id = i.id
WHERE i.is_sample_data = 1
  AND i.sku IS d.sku
  AND i.name = d.name
  AND i.base_price = d.base_price
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
  -- held_sales_archive (ut-docs#640 review follow-up): a held sale parked
  -- against a demo item, then swept into the archive by a reset before
  -- ever being tendered, is worse than the sale_lines/stock_movements gap
  -- above — held_sales_archive.payload carries no FK at all, so deleting
  -- the item here would let RestoreResetBatch succeed silently, and the
  -- shop owner would only discover the break as a raw "FOREIGN KEY
  -- constraint failed" the moment they try to tender the restored basket.
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
