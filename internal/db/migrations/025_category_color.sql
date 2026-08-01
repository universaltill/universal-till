-- Optional display color for a category, used by the sale-screen category
-- grid to color-code sections. NULL means "no explicit color set" — the UI
-- falls back to a deterministic auto-assigned color in that case.
ALTER TABLE categories ADD COLUMN color TEXT;
