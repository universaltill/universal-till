-- 077_kiosk_counter_orders_guest_order_id.sql — ut-docs#4337 (ADR-0159 §5):
-- a pay-at-counter order placed from the cloud guest page (a guest_order
-- directive) records the cloud's GuestOrder id. UNIQUE where set, so a
-- re-delivered directive can never park a second order for the same guest
-- order: the insert fails and the apply returns the first order's result.
-- NULL for every LAN/kiosk order (many coexist).
--
-- Replay-safe: the runner skips an ADD COLUMN whose column already exists
-- (ut-docs#1412), and the index is IF NOT EXISTS.
ALTER TABLE kiosk_counter_orders ADD COLUMN guest_order_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_kiosk_counter_orders_guest_order_id ON kiosk_counter_orders(guest_order_id) WHERE guest_order_id IS NOT NULL;
