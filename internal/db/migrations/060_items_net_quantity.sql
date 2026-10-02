-- 060_items_net_quantity.sql — ut-docs#3391 (Price Marking Order 2004, as
-- amended from 6 April 2026): a pre-packed item's shelf label can show its
-- unit price (per kg / per litre / per item), which needs the pack's net
-- quantity.
--
--   * net_quantity_value: the pack's net content in its smallest unit —
--     grams, millilitres, or a plain count for a multi-pack.
--   * net_quantity_unit: 'g' | 'ml' | 'ea'.
--
-- Both NULL (the state every existing item upgrades into) = no net quantity
-- configured, so no label changes. Nullable with no default, so the
-- column-level CHECK is accepted on ADD COLUMN.
ALTER TABLE items ADD COLUMN net_quantity_value INTEGER;
ALTER TABLE items ADD COLUMN net_quantity_unit TEXT CHECK (net_quantity_unit IN ('g','ml','ea'));
