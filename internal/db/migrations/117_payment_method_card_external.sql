-- 117_payment_method_card_external.sql — ut-docs#6755 (ADR-0205 §7 rung 0):
-- built-in tender for the shop's own, unconnected card terminal. The method id
-- is the per-payment "external" flag; the X/Z groups by it. Never plugin-owned.
INSERT OR IGNORE INTO payment_methods (id, name, type, is_active, sort_order, plugin_id)
VALUES ('card_external', 'Card (own terminal)', 'card', 1, 4, NULL);
