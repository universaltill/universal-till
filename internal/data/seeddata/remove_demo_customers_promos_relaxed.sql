-- Demo customer/promotion removal, RELAXED variant (ut-docs#1858). Same
-- script as remove_demo_customers_promos.sql (keep both in sync
-- deliberately — see that file's own header for the full explanation of
-- every safety clause below, none of which changed here) with exactly ONE
-- difference: the promotion "untouched" predicate drops the PRISTINE match
-- (type/value/description/is_active/starts_at/ends_at still equal to the
-- seeded values). DemoSeedRepo.RemoveDemoCustomersPromos picks this variant
-- instead of the strict one only when the till as a whole has no non-sample
-- trading history (demoTillHasNoRealHistorySQL, the same till-wide gate
-- RemoveDemoCatalogue already uses) — a shop that has never traded for real
-- has nothing for the pristine rule to protect, so a merchant who merely
-- deactivated a demo promo code while trying the till out can still remove
-- it in one tap, mirroring ut-docs#1840's fix for edited demo items exactly.
-- The moment the till has ANY real (non-sample) trading history,
-- RemoveDemoCustomersPromos falls back to the strict variant and an edited
-- demo promo is kept exactly as before — this file's relaxation never
-- reaches a till that has actually traded.
--
-- Demo CUSTOMERS have no pristine-match predicate in either variant — see
-- remove_demo_customers_promos.sql's own header for why a customer's
-- removability was never conditioned on staying pristine, only on not
-- being referenced. So the customer section below is byte-identical to the
-- strict file; only the promotion DELETE differs.
--
-- The customer_id IS NULL check on promotions is UNCONDITIONAL in both
-- variants, same as items' trading-history/held-basket clauses: a promo
-- targeted at a specific customer is a real, durable reference (the only
-- one this schema can track for a promo), not staleness, so relaxing it
-- would risk silently discarding a shop's own targeted-offer setup.
DROP TABLE IF EXISTS temp.demo_seed_customers_removable;
CREATE TEMP TABLE demo_seed_customers_removable AS
SELECT c.id FROM customers c
JOIN demo_seed_customers d ON d.id = c.id
WHERE c.is_sample_data = 1
  AND NOT EXISTS (SELECT 1 FROM sales s WHERE s.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM sales_archive s WHERE s.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM held_sales h WHERE h.payload LIKE '%"customer_id":"' || c.id || '"%')
  AND NOT EXISTS (SELECT 1 FROM held_sales_archive h WHERE h.payload LIKE '%"customer_id":"' || c.id || '"%')
  AND NOT EXISTS (SELECT 1 FROM promotions p WHERE p.customer_id = c.id);

-- Promotions, RELAXED: only the reference-based checks remain (targeted at
-- a customer). No field-pristine-match required — a merely-deactivated or
-- otherwise-edited demo promo is removable too, as long as it's still
-- untargeted.
DELETE FROM promotions
 WHERE code IN (SELECT code FROM demo_seed_promos)
   AND is_sample_data = 1
   AND customer_id IS NULL;

DELETE FROM customers
 WHERE id IN (SELECT id FROM demo_seed_customers_removable);

-- TEMP tables are per-connection: drop them so a later run on the same
-- pooled connection starts clean.
DROP TABLE IF EXISTS temp.demo_seed_customers_removable;
DROP TABLE IF EXISTS temp.demo_seed_customers;
DROP TABLE IF EXISTS temp.demo_seed_promos;
