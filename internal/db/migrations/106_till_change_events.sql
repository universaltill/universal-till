-- ADR-0200 (ut-docs#6559): the till's outbox of change events. Two kinds
-- only: 'version' (this till's software version changed) and 'plugin' (a
-- plugin was installed, updated or removed). Rows ride the next cloud sync
-- POST (oldest first, 50 at a time) and are deleted once the cloud answered
-- 2xx or 402. Bounded (200 rows, oldest dropped) by data.ChangeEventsRepo;
-- nothing here names a shop, a sale, a customer, a path or an error.
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
CREATE TABLE IF NOT EXISTS till_change_events (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    plugin_id    TEXT NOT NULL DEFAULT '',
    action       TEXT NOT NULL DEFAULT '',
    from_version TEXT NOT NULL DEFAULT '',
    to_version   TEXT NOT NULL DEFAULT '',
    at           TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_till_change_events_at ON till_change_events (at);
