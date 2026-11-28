-- Per-field version of a core setting (ut-docs shop-config design, group U3):
-- the cloud's optimistic concurrency for a remote edit. `ver` is monotonic per
-- key; `origin` says who wrote the current value (till | cloud); `at` is the
-- till's own clock and is display-only (ordering is by ver, never by time).
-- Rows exist only for typed-registry keys (data.AgentSettings), never for a
-- secret value (the value itself stays in `settings`). A key with no row
-- reads as ver 1, origin till.
CREATE TABLE IF NOT EXISTS setting_meta (
    key     TEXT PRIMARY KEY,
    ver     INTEGER NOT NULL DEFAULT 1 CHECK (ver >= 1),
    origin  TEXT NOT NULL DEFAULT 'till' CHECK (origin IN ('till', 'cloud')),
    at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
