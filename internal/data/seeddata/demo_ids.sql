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
