-- ut-docs#1900: reusable, named option sets ("Size: S / M / L") that
-- generate an item's item_variants range in one step, SumUp-style.
--
-- item_variants (001_init.sql) is a flat per-item list with a free-text
-- name typed by hand per row, so a shop selling 10 T-shirts in 4 sizes and
-- 3 colours types the same 12 names 10 times over — and there is nothing
-- linking "Small / Red" on one item to "Small / Red" on another. These
-- four tables add the reusable axis WITHOUT touching item_variants' own
-- shape: a generated variant is an ordinary item_variants row (sellable,
-- stockable, barcodeable through every existing path — no downstream
-- change), and item_variant_options is what records which option values
-- it was generated from so re-running the generator after adding a value
-- creates only the missing combinations (the exact re-import-duplication
-- class of bug ut-docs#1839 found in the pilot).
--
-- Checkout-time modifiers (item_modifier_groups/_options, ADR-0020) are a
-- separate, already-correct mechanism and are deliberately not involved.
CREATE TABLE IF NOT EXISTS option_sets (
    id        TEXT PRIMARY KEY,
    name      TEXT NOT NULL UNIQUE,
    is_active INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS option_set_values (
    id            TEXT PRIMARY KEY,
    option_set_id TEXT NOT NULL,
    value         TEXT NOT NULL,
    sort_order    INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (option_set_id) REFERENCES option_sets (id) ON DELETE CASCADE,
    UNIQUE (option_set_id, value)
);
CREATE INDEX IF NOT EXISTS idx_option_set_values_set ON option_set_values (option_set_id);

-- Which sets an item's range is generated from, in axis order (at most two
-- axes — enforced by OptionSetRepo.ApplyOptionSetsToItem, not the schema).
CREATE TABLE IF NOT EXISTS item_option_sets (
    item_id       TEXT NOT NULL,
    option_set_id TEXT NOT NULL,
    axis_order    INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE,
    FOREIGN KEY (option_set_id) REFERENCES option_sets (id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, option_set_id)
);

-- Which option values a generated variant is the combination of — the
-- generator's idempotency key. A hand-added variant has no rows here.
CREATE TABLE IF NOT EXISTS item_variant_options (
    variant_id          TEXT NOT NULL,
    option_set_value_id TEXT NOT NULL,
    FOREIGN KEY (variant_id) REFERENCES item_variants (id) ON DELETE CASCADE,
    FOREIGN KEY (option_set_value_id) REFERENCES option_set_values (id) ON DELETE CASCADE,
    PRIMARY KEY (variant_id, option_set_value_id)
);
