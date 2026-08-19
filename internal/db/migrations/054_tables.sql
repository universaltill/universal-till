-- Table floor plan (universaltill/ut-docs#814, ADR-0054): per-store dining
-- tables laid out on a 2D map. Positions are integer coordinates on a fixed
-- 1000×1000 LOGICAL canvas (not raw pixels — the SVG viewBox scales the same
-- layout to a phone or a tablet), owned by the floor-plan editor page.
--
-- The till database is per-store (same convention as kitchen_stations —
-- no store column), and tables are soft-disabled via `enabled`, never
-- hard-deleted, so future order history referencing a table never orphans.
-- Timestamps are RFC3339 TEXT like every other table here.
CREATE TABLE tables (
    id         TEXT PRIMARY KEY,
    label      TEXT NOT NULL,                -- table number or name ("T1", "Window 2")
    area_zone  TEXT NOT NULL DEFAULT '',     -- free-text area/zone ("Terrace", "Main room")
    seat_count INTEGER NOT NULL DEFAULT 0,
    shape      TEXT NOT NULL DEFAULT 'rect' CHECK (shape IN ('rect','round')),
    pos_x      INTEGER NOT NULL DEFAULT 0,   -- logical-canvas units, 0..1000
    pos_y      INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Forward-compat column for the follow-on order-assignment slice
-- (ut-docs#820), same move 034 made by admitting destination_type 'display'
-- before the KDS slice existed: the free/occupied live-state query LEFT JOINs
-- held_sales on this column TODAY, but NOTHING writes it until #820 ships —
-- so every table correctly reads as free, and #820 needs no schema rework.
ALTER TABLE held_sales ADD COLUMN table_id TEXT REFERENCES tables(id);
CREATE INDEX idx_held_sales_table ON held_sales(table_id);
