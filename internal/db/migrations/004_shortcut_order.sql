-- Cashier-defined ordering of the product shortcut tiles (Designer drag&drop).
ALTER TABLE shortcut_buttons ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;
