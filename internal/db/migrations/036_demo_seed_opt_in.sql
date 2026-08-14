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
-- DROP first: TEMP tables are per-connection and a pooled connection may
-- have run this script before.
DROP TABLE IF EXISTS temp.demo_seed_items;
CREATE TEMP TABLE demo_seed_items (id TEXT PRIMARY KEY);
INSERT INTO demo_seed_items (id) VALUES
  ('itm001'), ('itm002'), ('itm003'), ('itm004'), ('itm005'),
  ('itm006'), ('itm007'), ('itm008'), ('itm009'), ('itm010'),
  ('itm011'), ('itm012'), ('itm013'), ('itm014'), ('itm015'),
  ('itm016'), ('itm017'), ('itm018'), ('itm019'), ('itm020'),
  ('itm021'), ('itm022'), ('itm023'), ('itm024'), ('itm025'),
  ('itm026'), ('itm027'), ('itm028'), ('itm029'), ('itm030'),
  ('itm031'), ('itm032'), ('itm033'), ('itm034'), ('itm035'),
  ('itm036'), ('itm037'), ('itm038'), ('itm039'), ('itm040'),
  ('itm041'), ('itm042'), ('itm043'), ('itm044'), ('itm045'),
  ('itm046'), ('itm047'), ('itm048'), ('itm049'), ('itm050');

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
-- flagged is_sample_data = 1 AND nothing in sale_lines or stock_movements
-- references it — directly or through one of its variants (sale_lines and
-- stock_movements have no ON DELETE CASCADE, so deleting a sold/adjusted
-- item would either fail the FK or orphan real trading history) — AND no
-- HELD (parked) sale references it either, the same signal
-- remove_demo_customers_promos.sql already checks for demo customers
-- (ut-docs#633): a held_sales row is an in-progress basket that hasn't
-- reached sale_lines/stock_movements yet, but still FK-fails on tender if
-- the item (or variant) it was parked against no longer exists. A demo
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
