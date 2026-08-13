-- 038_demo_customers_promos_opt_in.sql (ut-docs#567)
--
-- 001_init.sql unconditionally seeds 3 demo customers (Alice Carter/Ben
-- Singh/Chloe Martin) and 3 live, applicable promo codes (PROMO50/PROMO500/
-- DISC10 — DISC10 alone is 10% off the whole basket) into every install.
-- Migration 036 (ut-docs#539) already made the demo item catalogue opt-in
-- and gave Settings a "Remove sample data" action, but explicitly left
-- these out of scope ("Sample customers/promotions from 001 are also out
-- of this card's scope (catalogue only)"). A shop owner tapping "Remove
-- sample data" reasonably expects a working 10%-off code to go with it; it
-- didn't, and a redeemable discount code stayed live indefinitely on any
-- till that never explicitly cleared it. This migration closes that gap:
-- customers/promotions get the exact same opt-in treatment the catalogue
-- got (the two shared blocks below are byte-identical copies of
-- demo_customers_promos_ids.sql and remove_demo_customers_promos.sql in
-- internal/data/seeddata, guarded by TestMigration038MatchesSeedData).
--
-- On a brand-new install this runs microseconds after 001, before the
-- operator ever sees the till, so nothing can have referenced the demo
-- rows yet and they are all removed — the same "fresh install ends up
-- clean" outcome 036 established for the catalogue. On an existing till
-- upgrading, the "untouched" rule below (different in shape from the
-- catalogue's — see remove_demo_customers_promos.sql's own header for why)
-- keeps anything the shop actually used.

-- is_sample_data lets Settings report these rows as sample data and lets
-- the removal path target only rows the opt-in seed inserted — same
-- convention items already use (migration 036).
ALTER TABLE customers ADD COLUMN is_sample_data INTEGER NOT NULL DEFAULT 0;
ALTER TABLE promotions ADD COLUMN is_sample_data INTEGER NOT NULL DEFAULT 0;

-- ---- shared block 1/2: internal/data/seeddata/demo_customers_promos_ids.sql ----
-- Demo customer/promotion ID lists (ut-docs#567) — loaded into TEMP tables
-- so the removal script (remove_demo_customers_promos.sql) can target the
-- demo rows precisely by ID/code instead of guessing with LIKE patterns.
-- Shared verbatim by migration 038_demo_customers_promos_opt_in.sql and
-- DemoSeedRepo.RemoveDemoCustomersPromos.
-- DROP first: TEMP tables are per-connection and a pooled connection may
-- have run this script before.
DROP TABLE IF EXISTS temp.demo_seed_customers;
CREATE TEMP TABLE demo_seed_customers (id TEXT PRIMARY KEY);
INSERT INTO demo_seed_customers (id) VALUES
  ('cust-001'), ('cust-002'), ('cust-003');

DROP TABLE IF EXISTS temp.demo_seed_promos;
CREATE TEMP TABLE demo_seed_promos (code TEXT PRIMARY KEY);
INSERT INTO demo_seed_promos (code) VALUES
  ('PROMO50'), ('PROMO500'), ('DISC10');

-- Flag the legacy 001-seeded rows (surviving touched rows keep the flag, so
-- Settings can still report them as sample data that could not be removed).
UPDATE customers SET is_sample_data = 1 WHERE id IN (SELECT id FROM demo_seed_customers);
UPDATE promotions SET is_sample_data = 1 WHERE code IN (SELECT code FROM demo_seed_promos);

-- ---- shared block 2/2: internal/data/seeddata/remove_demo_customers_promos.sql ----
-- Demo customer/promotion removal (ut-docs#567). Requires
-- demo_customers_promos_ids.sql to have run first on the same connection
-- (it creates the temp demo_seed_customers / demo_seed_promos ID tables).
-- Shared verbatim by migration 038_demo_customers_promos_opt_in.sql and
-- DemoSeedRepo.RemoveDemoCustomersPromos.
--
-- "Untouched" safety rule — deliberately not the same shape as the
-- catalogue's (demo_ids.sql/remove_demo.sql), because this schema can't
-- support the same check:
--  - A demo CUSTOMER is removable only when still flagged
--    is_sample_data = 1 AND not referenced by any sale (sales.customer_id),
--    any HELD (parked) sale (held_sales.payload — an in-progress basket
--    that hasn't reached the sales table yet, but still FK-fails on tender
--    if the customer it was parked against no longer exists; independent
--    review, ut-docs#567), or any promotion's targeting
--    (promotions.customer_id) — i.e. never actually used at checkout,
--    mid-checkout, or singled out for a targeted offer. This mirrors the
--    catalogue's "never sold/adjusted" rule; all three are real, durable
--    FK- or payload-backed signals.
--  - A demo PROMOTION is removable only when still flagged
--    is_sample_data = 1 AND not currently targeted at a specific customer
--    (customer_id IS NULL) AND every other field still matches its 001
--    seed default exactly (type/value/description/is_active/starts_at/
--    ends_at). Unlike an item, a promo code's redemption at the till
--    leaves no durable link back to this table — sale_discounts records
--    only the resulting discount amount, not which code produced it (and
--    the product has no promotions management UI at all yet, so
--    customer_id is the ONLY way a promo could ever be deliberately
--    targeted) — so "untouched" is judged by whether the row still reads
--    exactly as seeded, the closest available proxy for "the shop hasn't
--    relied on or customized this," not by redemption history, which this
--    schema has no way to recover.
DROP TABLE IF EXISTS temp.demo_seed_customers_removable;
CREATE TEMP TABLE demo_seed_customers_removable AS
SELECT c.id FROM customers c
JOIN demo_seed_customers d ON d.id = c.id
WHERE c.is_sample_data = 1
  AND NOT EXISTS (SELECT 1 FROM sales s WHERE s.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM held_sales h WHERE h.payload LIKE '%"customer_id":"' || c.id || '"%')
  AND NOT EXISTS (SELECT 1 FROM promotions p WHERE p.customer_id = c.id);

-- Promotions: a targeted demo promotion (customer_id set — to a demo
-- customer or a real one) is kept regardless of order here, because the
-- customer-removable set above was already computed from the pre-delete
-- state — targeting a demo customer doesn't stop that customer from being
-- correctly kept (it's "referenced by a promotion" either way).
DELETE FROM promotions
 WHERE code IN (SELECT code FROM demo_seed_promos)
   AND is_sample_data = 1
   AND customer_id IS NULL
   AND is_active = 1
   AND starts_at IS NULL
   AND ends_at IS NULL
   AND (
        (code = 'PROMO50'  AND type = 'amount'  AND value = 50   AND description = '50p off')
     OR (code = 'PROMO500' AND type = 'amount'  AND value = 500  AND description = '£5 off')
     OR (code = 'DISC10'   AND type = 'percent' AND value = 1000 AND description = '10% off basket')
   );

DELETE FROM customers
 WHERE id IN (SELECT id FROM demo_seed_customers_removable);

-- TEMP tables are per-connection: drop them so a later run on the same
-- pooled connection starts clean.
DROP TABLE IF EXISTS temp.demo_seed_customers_removable;
DROP TABLE IF EXISTS temp.demo_seed_customers;
DROP TABLE IF EXISTS temp.demo_seed_promos;
