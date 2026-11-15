-- 092_tills_receipt_prefix.sql — ut-docs#5058 UT-5: the receipt prefix
-- (T<n>-) the main till gave each enrolled till. Until now it was derived
-- from the number of tills at enrolment and not kept, so when the tills of a
-- shop re-joined a NEW main till in another order, one could get a prefix
-- another till's queued sales already use, and those sales then collided on
-- sales.receipt_no. The main till now records the prefix it grants and keeps
-- a re-joining till's own prefix when no other till holds it or has sales
-- under it. '' for tills enrolled before this migration.
ALTER TABLE tills ADD COLUMN receipt_prefix TEXT NOT NULL DEFAULT '';

-- It travels in the admin bundle with the rest of the roster (a till
-- promoted to main till later keeps knowing which prefixes are taken), so a
-- change bumps the admin generation like role does (migration 067).
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tills_prefix_upd AFTER UPDATE OF receipt_prefix ON tills
WHEN OLD.receipt_prefix IS NOT NEW.receipt_prefix
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
