-- 017: item modifiers (ADR-0020, spec 011) — customization options attached
-- to a catalog item (e.g. "Extras: add shot +£0.50", "Bread: white/wheat/rye"),
-- shared by the cashier till and the (future) self-order kiosk. A sale
-- line's chosen modifiers are SNAPSHOTTED at time of sale (name + price
-- delta), never a live FK to the option row — a later catalog edit must
-- never rewrite a past receipt.

CREATE TABLE IF NOT EXISTS item_modifier_groups (
    id          TEXT PRIMARY KEY,
    item_id     TEXT NOT NULL,
    name        TEXT NOT NULL,
    required    INTEGER NOT NULL DEFAULT 0,   -- 1 = customer must pick within min/max
    min_select  INTEGER NOT NULL DEFAULT 0,
    max_select  INTEGER NOT NULL DEFAULT 1,   -- 1 = single-pick, >1 = multi-pick
    sort_order  INTEGER NOT NULL DEFAULT 0,
    is_active   INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE,
    CHECK (min_select >= 0 AND max_select >= min_select)
);

CREATE INDEX IF NOT EXISTS idx_item_modifier_groups_item
    ON item_modifier_groups (item_id);

CREATE TABLE IF NOT EXISTS item_modifier_options (
    id                TEXT PRIMARY KEY,
    group_id          TEXT NOT NULL,
    name              TEXT NOT NULL,
    price_delta_minor INTEGER NOT NULL DEFAULT 0,  -- minor units, additive-only (v1)
    sort_order        INTEGER NOT NULL DEFAULT 0,
    is_active         INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (group_id) REFERENCES item_modifier_groups (id) ON DELETE CASCADE,
    CHECK (price_delta_minor >= 0)
);

CREATE INDEX IF NOT EXISTS idx_item_modifier_options_group
    ON item_modifier_options (group_id);

-- One row per chosen option on a sale line. Snapshotted, not FK'd to the
-- option's current state — group_id/option_id are kept only for reporting
-- ("how often was extra shot picked"), never re-read for pricing.
CREATE TABLE IF NOT EXISTS sale_line_modifiers (
    id                     TEXT PRIMARY KEY,
    sale_line_id           TEXT NOT NULL,
    group_id               TEXT,             -- nullable: source group may since be deleted
    option_id              TEXT,             -- nullable: source option may since be deleted
    group_name_snapshot    TEXT NOT NULL,
    option_name_snapshot   TEXT NOT NULL,
    price_delta_minor      INTEGER NOT NULL,
    FOREIGN KEY (sale_line_id) REFERENCES sale_lines (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sale_line_modifiers_line
    ON sale_line_modifiers (sale_line_id);
