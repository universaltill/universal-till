-- 069_customers_phone_e164_notes.sql — ut-docs#3200 (ADR-0131 §3–§4,
-- caller ID card 1): customers gain a normalised phone number for caller-ID
-- lookup, and a free-text notes field (#1602 owns the create/edit form).
--
-- customers.phone_e164 holds what internal/phonenumber.Normalise makes of
-- customers.phone, with the shop's country (settings store.country) as the
-- default region:
--   '+<digits>'  E.164, e.g. '+442079460018';
--   '<digits>'   digits only, when phone could not be normalised (matched
--                by its trailing digits instead);
--   ''           the customer has no phone;
--   NULL         not computed yet.
-- data.BackfillCustomerPhoneE164 fills NULL rows in chunks in the background
-- after start-up, on every till, and recomputes every row when the shop's
-- country changes. Readers treat NULL as "normalise phone in Go".
--
-- The trigger below resets phone_e164 to NULL whenever phone changes on its
-- own, so a stale value never outlives the number it was computed from. A
-- write that changes both (a main till's admin-sync row) keeps the value it
-- wrote; one that changes phone but writes back the same phone_e164 (a
-- formatting-only edit) gets NULL, which the back-fill recomputes.
--
-- Both columns are personal data (ADR-0046, regional): they travel to LAN
-- replicas with the rest of the customers row and are blanked on a
-- replica's erased-customer shell (sync_admin_repo.go scrubOnRetire). They
-- never go to the cloud.
--
-- idx_sales_customer_id serves the pop-up's "last 5 sales" per customer
-- (POSRepo.LookupCustomersByPhone); sales had no index on customer_id.
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
ALTER TABLE customers ADD COLUMN phone_e164 TEXT;
ALTER TABLE customers ADD COLUMN notes TEXT;

CREATE INDEX IF NOT EXISTS idx_customers_phone_e164 ON customers (phone_e164);
CREATE INDEX IF NOT EXISTS idx_sales_customer_id ON sales (customer_id);

CREATE TRIGGER IF NOT EXISTS trg_customers_phone_e164_reset AFTER UPDATE OF phone ON customers
WHEN NEW.phone IS NOT OLD.phone AND NEW.phone_e164 IS OLD.phone_e164
BEGIN
  UPDATE customers SET phone_e164 = NULL WHERE id = NEW.id;
END;
