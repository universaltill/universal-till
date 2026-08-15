-- 036_demo_seed_opt_in.sql (ut-docs#539)
--
-- 001_init.sql unconditionally seeded a 50-item grocery demo catalogue into
-- every install. That seed is now OPT-IN: this migration removes every demo
-- row the shop never touched, and the setup wizard / Settings can re-seed it
-- on request (internal/data/seeddata is the single source of truth; the two
-- shared blocks below are byte-identical copies of demo_ids.sql and
-- remove_demo.sql there, guarded by TestMigration036MatchesSeedData).
--
-- On a brand-new install this runs microseconds after 001, before the
-- operator ever sees the till, so nothing can have referenced the demo rows
-- yet and they are all removed. On an existing till upgrading, the
-- "untouched" rule below keeps anything the shop actually traded with: an
-- item referenced by sale_lines or stock_movements (directly or via a
-- variant) survives, and so do its category and brand.
--
-- tax_codes (tax_std/tax_red/tax_zero) and stock_locations (loc_main/
-- loc_back/loc_wh) are structural defaults every till needs, NOT demo data —
-- untouched here. Sample customers/promotions from 001 are also out of this
-- card's scope (catalogue only).

-- is_sample_data lets the UI badge sample rows and lets the removal path
-- target only rows the opt-in seed inserted (INTEGER 0/1, house style for
-- booleans in this schema).
ALTER TABLE items ADD COLUMN is_sample_data INTEGER NOT NULL DEFAULT 0;

-- ---- shared block 1/2: internal/data/seeddata/demo_ids.sql ----
-- Demo catalogue ID lists (ut-docs#539) — loaded into TEMP tables so the
-- removal script (remove_demo.sql) can target the demo rows precisely by ID
-- instead of guessing with LIKE patterns. Shared verbatim by migration
-- 036_demo_seed_opt_in.sql and DemoSeedRepo.RemoveDemoCatalogue; the ID
-- lists are guarded against demo_catalogue.sql by tests in this package.
--
-- demo_seed_items also carries each item's seeded sku/name/base_price
-- (ut-docs#566) — the "pristine" reference values remove_demo.sql compares
-- a live item against, so a demo item the shop renamed/repriced (but never
-- sold or stock-adjusted) is recognised as touched and kept, not just one
-- with trading history. Values are a literal copy of demo_catalogue.sql's
-- items INSERT, same duplication convention as the ID lists themselves;
-- guarded against drift by TestMigration036MatchesSeedData.
-- DROP first: TEMP tables are per-connection and a pooled connection may
-- have run this script before.
DROP TABLE IF EXISTS temp.demo_seed_items;
CREATE TEMP TABLE demo_seed_items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, base_price INTEGER);
INSERT INTO demo_seed_items (id, sku, name, base_price) VALUES
  ('itm001', 'SKU-0001', 'Coca-Cola Can 330ml', 120),
  ('itm002', 'SKU-0002', 'Pepsi Can 330ml', 115),
  ('itm003', 'SKU-0003', 'Sparkling Water 500ml', 85),
  ('itm004', 'SKU-0004', 'Still Water 1.5L', 110),
  ('itm005', 'SKU-0005', 'Orange Juice 1L', 220),
  ('itm006', 'SKU-0006', 'Apple Juice 1L', 210),
  ('itm007', 'SKU-0007', 'Semi-Skimmed Milk 2L', 190),
  ('itm008', 'SKU-0008', 'Whole Milk 1L', 110),
  ('itm009', 'SKU-0009', 'Butter 250g', 215),
  ('itm010', 'SKU-0010', 'Cheddar Cheese 400g', 320),
  ('itm011', 'SKU-0011', 'White Bread Loaf', 140),
  ('itm012', 'SKU-0012', 'Brown Bread Loaf', 150),
  ('itm013', 'SKU-0013', 'Croissant Pack x4', 200),
  ('itm014', 'SKU-0014', 'Chocolate Muffin x2', 180),
  ('itm015', 'SKU-0015', 'Walkers Ready Salted 45g', 95),
  ('itm016', 'SKU-0016', 'Walkers Cheese & Onion 45g', 95),
  ('itm017', 'SKU-0017', 'Salted Peanuts 200g', 170),
  ('itm018', 'SKU-0018', 'Mixed Nuts 200g', 240),
  ('itm019', 'SKU-0019', 'Milk Chocolate Bar 100g', 130),
  ('itm020', 'SKU-0020', 'Dark Chocolate Bar 100g', 140),
  ('itm021', 'SKU-0021', 'Kellogg''s Cornflakes 500g', 280),
  ('itm022', 'SKU-0022', 'Oat Granola 750g', 340),
  ('itm023', 'SKU-0023', 'Frozen Peas 1kg', 160),
  ('itm024', 'SKU-0024', 'Frozen Chips 1.5kg', 210),
  ('itm025', 'SKU-0025', 'Vanilla Ice Cream 1L', 350),
  ('itm026', 'SKU-0026', 'Bananas', 120),
  ('itm027', 'SKU-0027', 'Apples', 180),
  ('itm028', 'SKU-0028', 'Tomatoes', 210),
  ('itm029', 'SKU-0029', 'Onions', 90),
  ('itm030', 'SKU-0030', 'Potatoes 2.5kg Bag', 260),
  ('itm031', 'SKU-0031', 'Heinz Baked Beans 415g', 140),
  ('itm032', 'SKU-0032', 'Tomato Ketchup 460g', 250),
  ('itm033', 'SKU-0033', 'Pasta Spaghetti 500g', 125),
  ('itm034', 'SKU-0034', 'Rice Basmati 1kg', 210),
  ('itm035', 'SKU-0035', 'Olive Oil 500ml', 520),
  ('itm036', 'SKU-0036', 'Laundry Detergent 1.5L', 650),
  ('itm037', 'SKU-0037', 'Dishwashing Liquid 500ml', 180),
  ('itm038', 'SKU-0038', 'Paper Towels x2', 220),
  ('itm039', 'SKU-0039', 'Toilet Paper x9', 590),
  ('itm040', 'SKU-0040', 'All-Purpose Cleaner 750ml', 240),
  ('itm041', 'SKU-0041', 'Shampoo 400ml', 310),
  ('itm042', 'SKU-0042', 'Conditioner 400ml', 310),
  ('itm043', 'SKU-0043', 'Soap Bar 2x100g', 160),
  ('itm044', 'SKU-0044', 'Toothpaste 75ml', 210),
  ('itm045', 'SKU-0045', 'Toothbrush Medium', 140),
  ('itm046', 'SKU-0046', 'Energy Drink 250ml', 155),
  ('itm047', 'SKU-0047', 'Protein Bar 60g', 210),
  ('itm048', 'SKU-0048', 'Instant Coffee 200g', 460),
  ('itm049', 'SKU-0049', 'Tea Bags x80', 295),
  ('itm050', 'SKU-0050', 'Sugar 1kg', 135);

DROP TABLE IF EXISTS temp.demo_seed_categories;
CREATE TEMP TABLE demo_seed_categories (id TEXT PRIMARY KEY);
INSERT INTO demo_seed_categories (id) VALUES
  ('cat_bakery'), ('cat_clean'), ('cat_dairy'), ('cat_drink'), ('cat_food'),
  ('cat_frozen'), ('cat_house'), ('cat_personal'), ('cat_produce'), ('cat_snack');

DROP TABLE IF EXISTS temp.demo_seed_brands;
CREATE TEMP TABLE demo_seed_brands (id TEXT PRIMARY KEY);
INSERT INTO demo_seed_brands (id) VALUES
  ('br_coca'), ('br_generic'), ('br_heinz'), ('br_kell'),
  ('br_nestle'), ('br_pepsi'), ('br_unilev'), ('br_walk');

-- Flag the legacy 001-seeded rows (surviving touched items keep the flag, so
-- Settings can still report them as sample data that could not be removed).
UPDATE items SET is_sample_data = 1 WHERE id IN (SELECT id FROM demo_seed_items);

-- ---- shared block 2/2: internal/data/seeddata/remove_demo.sql ----
-- Demo catalogue removal (ut-docs#539). Requires demo_ids.sql to have run
-- first on the same connection (it creates the temp demo_seed_* ID tables).
-- Shared verbatim by migration 036_demo_seed_opt_in.sql and
-- DemoSeedRepo.RemoveDemoCatalogue.
--
-- "Untouched" safety rule: a demo item may only be deleted when it is still
-- flagged is_sample_data = 1 AND still PRISTINE — its sku/name/base_price
-- still match the values it was seeded with (ut-docs#566: a shop that
-- renamed/repriced a demo item before ever selling or stock-adjusting it
-- has already made it real, and trading history alone missed that) — AND
-- nothing in sale_lines or stock_movements references it — directly or
-- through one of its variants (sale_lines and stock_movements have no ON
-- DELETE CASCADE, so deleting a sold/adjusted item would either fail the FK
-- or orphan real trading history) — AND no HELD (parked) sale references it
-- either, the same signal remove_demo_customers_promos.sql already checks
-- for demo customers (ut-docs#633): a held_sales row is an in-progress
-- basket that hasn't reached sale_lines/stock_movements yet, but still
-- FK-fails on tender if the item (or variant) it was parked against no
-- longer exists. A demo category/brand goes only when, after the item pass,
-- no remaining row (demo or operator-created) still references it.
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
  AND NOT EXISTS (SELECT 1 FROM held_sales h
                  WHERE h.payload LIKE '%"item_id":"' || i.id || '"%')
  AND NOT EXISTS (SELECT 1 FROM held_sales h
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
