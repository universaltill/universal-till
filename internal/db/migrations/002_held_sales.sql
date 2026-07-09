-- Held (parked) sales: a customer's in-progress basket saved so the till can
-- serve someone else and resume later. Payload is the serialized basket
-- snapshot (lines, discount, customer) produced by pos.Service.Snapshot.
CREATE TABLE IF NOT EXISTS held_sales (
    id          TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    total_minor INTEGER NOT NULL DEFAULT 0,
    line_count  INTEGER NOT NULL DEFAULT 0,
    payload     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
