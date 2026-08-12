-- Kitchen station routing (universaltill/ut-docs#516): named prep stations
-- ("Grill", "Bar", "Dessert") each with their own printer, plus routing rules
-- that decide which station(s) a sold item's kitchen ticket line goes to.
--
-- destination_type admits 'display' on purpose so the Kitchen Display slice
-- (ut-docs#544) needs no schema rework, but THIS slice only creates and uses
-- 'printer' stations — there is no UI for 'display' yet.
--
-- Routing precedence (POSRepo.ResolveKitchenStations owns the algorithm):
-- item_station_routes rows OVERRIDE category_station_routes (no union); an
-- item with neither falls back to the legacy default kitchen printer
-- (setting printer.kitchen_addr), so a shop with zero stations behaves
-- exactly as before this migration. Stations are soft-disabled via
-- `enabled` (mirrors stock_locations.is_active) — no hard delete, so
-- routing rows never orphan. Timestamps are RFC3339 TEXT like every other
-- table here.
CREATE TABLE kitchen_stations (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    destination_type TEXT NOT NULL DEFAULT 'printer' CHECK (destination_type IN ('printer','display')),
    printer_address  TEXT,
    enabled          INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

-- ON DELETE CASCADE on both FKs (code review, ut-docs#516): matches the
-- existing convention for items' other operational children (barcodes,
-- images, variants) and keeps POSRepo.CleanupObsoleteItems (which hard-
-- deletes inactive, never-sold items and already pre-deletes children
-- lacking a cascade) from hitting a raw FK-constraint failure the moment an
-- item carries a station override. category_station_routes cascades for the
-- same reason if a category is ever hard-deleted.
CREATE TABLE item_station_routes (
    item_id    TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    station_id TEXT NOT NULL REFERENCES kitchen_stations(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, station_id)
);
CREATE INDEX idx_item_station_routes_station ON item_station_routes(station_id);

CREATE TABLE category_station_routes (
    category_id TEXT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    station_id  TEXT NOT NULL REFERENCES kitchen_stations(id) ON DELETE CASCADE,
    PRIMARY KEY (category_id, station_id)
);
CREATE INDEX idx_category_station_routes_station ON category_station_routes(station_id);
