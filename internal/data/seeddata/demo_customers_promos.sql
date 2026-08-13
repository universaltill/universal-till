-- Demo customers + promo codes (ut-docs#567) — the opt-in "sample data"
-- companions to demo_catalogue.sql (ut-docs#539): same 3 customers and 3
-- promo codes 001_init.sql used to seed unconditionally, now inserted only
-- on request (the setup wizard's sample-data checkbox — the only caller;
-- Settings → Data only ever removes sample data, it has no re-seed
-- action), every row flagged is_sample_data = 1. INSERT OR IGNORE
-- throughout, so it's idempotent and never fails on an operator's
-- clashing row.
INSERT OR IGNORE INTO customers (id, name, phone, email, address, loyalty_no, is_sample_data) VALUES
('cust-001', 'Alice Carter', '+441234567890', 'alice@example.com', '10 High St, London', 'LOY-ALICE', 1),
('cust-002', 'Ben Singh', '+441122334455', 'ben@example.com', '22 Market Rd, Manchester', 'LOY-BEN', 1),
('cust-003', 'Chloe Martin', '+447700900123', 'chloe@example.com', '5 Queen Sq, Bristol', 'LOY-CHLOE', 1);

-- amount = minor units, percent = basis points (1000 = 10.00%)
INSERT OR IGNORE INTO promotions (code, type, value, description, is_active, is_sample_data) VALUES
('PROMO50', 'amount', 50, '50p off', 1, 1),
('PROMO500', 'amount', 500, '£5 off', 1, 1),
('DISC10', 'percent', 1000, '10% off basket', 1, 1);
