-- 024: service_charge_amount on sales (ut-docs#72, "Service charge field on
-- sales, distinct from tip"). A restaurant service charge is a till-set
-- percentage automatically added to the bill -- unlike tip_amount (metadata
-- on a payment, excluded from the sale total), a service charge IS revenue
-- the customer owes, so it lives on the sale itself and participates in the
-- total/payment-sufficiency check. The computed amount is persisted (not
-- just the rate) so a later change to the till's configured rate never
-- retroactively changes a historical sale's total.
ALTER TABLE sales ADD COLUMN service_charge_amount INTEGER NOT NULL DEFAULT 0;
