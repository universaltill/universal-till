-- Demo customer/promotion removal (ut-docs#567). Requires
-- demo_customers_promos_ids.sql to have run first on the same connection
-- (it creates the temp demo_seed_customers / demo_seed_promos ID tables).
-- Executed verbatim by DemoSeedRepo.RemoveDemoCustomersPromos (until the
-- ADR-0074 migration squash, ut-docs#1425, this was also shared verbatim
-- by migration 038_demo_customers_promos_opt_in.sql, now deleted).
--
-- "Untouched" safety rule — deliberately not the same shape as the
-- catalogue's (demo_ids.sql/remove_demo.sql), because this schema can't
-- support the same check:
--  - A demo CUSTOMER is removable only when still flagged
--    is_sample_data = 1 AND not referenced by any sale, LIVE OR ARCHIVED
--    (sales.customer_id / sales_archive.customer_id — ut-docs#640: right
--    after a reset-transactions run, the live sales table is empty and the
--    real reference sits in sales_archive instead; without this a demo
--    customer an archived batch still points to could be deleted here, and
--    a later RestoreResetBatch would then hit a live FK it can no longer
--    satisfy — the same gap ErrArchiveReferencesRemoved's doc comment
--    already names this exact button as a cause of), any HELD (parked)
--    sale (held_sales.payload — an in-progress basket that hasn't reached
--    the sales table yet, but still FK-fails on tender if the customer it
--    was parked against no longer exists; independent review, ut-docs#567),
--    or any promotion's targeting (promotions.customer_id) — i.e. never
--    actually used at checkout, mid-checkout, or singled out for a
--    targeted offer. This mirrors the catalogue's "never sold/adjusted"
--    rule; all four are real, durable FK- or payload-backed signals.
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
  AND NOT EXISTS (SELECT 1 FROM sales_archive s WHERE s.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM held_sales h WHERE h.payload LIKE '%"customer_id":"' || c.id || '"%')
  -- held_sales_archive (ut-docs#640 review follow-up): same reasoning as
  -- remove_demo.sql's own held_sales_archive clause — a payload with no FK
  -- at all, so a stale reference here breaks silently at restore-then-
  -- tender time rather than being caught up front.
  AND NOT EXISTS (SELECT 1 FROM held_sales_archive h WHERE h.payload LIKE '%"customer_id":"' || c.id || '"%')
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
