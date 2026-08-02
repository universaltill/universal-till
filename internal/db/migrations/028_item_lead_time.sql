-- Per-item reorder lead time (universaltill/ut-docs#85): how many days it
-- takes to receive a reorder for this item. 0 = unset, and the inventory
-- page's warn/reorder-suggestion thresholds fall back to their existing
-- flat defaults (see internal/pages/inventory_page.go).
ALTER TABLE items ADD COLUMN lead_time_days INTEGER NOT NULL DEFAULT 0;
