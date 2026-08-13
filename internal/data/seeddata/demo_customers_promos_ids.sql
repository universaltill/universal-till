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
