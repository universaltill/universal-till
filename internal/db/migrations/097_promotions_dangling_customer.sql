-- 097_promotions_dangling_customer.sql (ut-docs#5590)
--
-- Older tills ran without foreign-key enforcement (db.go DSN fix), so a
-- promotion can point at a customers row that no longer exists. A NULL
-- customer_id means "any customer" (activePromoWhere), so merely NULLing it
-- would widen a one-customer discount to everyone. Instead deactivate AND
-- unlink: such a promo is already inert at checkout, so nothing redeemable
-- changes, and a manager can knowingly re-activate it. One statement, so both
-- columns change atomically. NOT EXISTS (not NOT IN): customers.id may hold a
-- NULL, which would make NOT IN match nothing. Idempotent; the 023 trigger
-- bumps sync_admin_version only when a row is actually repaired.
UPDATE promotions SET is_active = 0, customer_id = NULL
WHERE customer_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM customers c WHERE c.id = promotions.customer_id);
