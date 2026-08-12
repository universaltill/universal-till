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
