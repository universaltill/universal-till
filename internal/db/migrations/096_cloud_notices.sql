-- 096_cloud_notices.sql — ut-docs#5366 (ADR-0176): notices the cloud sends to
-- this till, pulled on the check-in and kept here so the bell, the inbox, the
-- toast and the critical strip read local data only (offline-first: the sale
-- path never touches this table).
--
-- The table mirrors the set the cloud last listed for this device
-- (internal/data/cloud_notices_repo.go): ids the cloud no longer lists are
-- deleted, so expiry, withdrawal and a partner mute need no tombstones. It is
-- bounded in code (100 rows / 256 KiB, eviction order in ADR-0176 §3) and
-- holds no personal data. read_at / dismissed_at / acked_at are this till's
-- own marks; reported_at is when they were last sent to the cloud as acks.
-- toasted_at remembers that the one non-modal toast for this id+rev was shown.
--
-- notices.view (ADR-0176 §7, cashier is sale-only): the bell, the inbox and
-- the read/dismiss/acknowledge writes. Seeded like stock_management:
-- admin, manager and super_admin granted, cashier not. An admin can grant it
-- in Users -> Permissions.
--
-- Safe on a database that already has data: a new table and two new rows.
-- Migrations are frozen the moment they merge (ADR-0100).

CREATE TABLE IF NOT EXISTS cloud_notices (
    id           TEXT PRIMARY KEY,
    rev          INTEGER NOT NULL DEFAULT 0,
    sender_kind  TEXT NOT NULL,
    sender_name  TEXT NOT NULL,
    severity     TEXT NOT NULL,
    lang         TEXT NOT NULL,
    title        TEXT NOT NULL,
    body         TEXT NOT NULL,
    link_json    TEXT NOT NULL DEFAULT '',
    starts_at    TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    received_at  TEXT NOT NULL,
    read_at      TEXT,
    dismissed_at TEXT,
    acked_at     TEXT,
    toasted_at   TEXT,
    reported_at  TEXT
);
CREATE INDEX IF NOT EXISTS idx_cloud_notices_expires ON cloud_notices (expires_at);

INSERT OR IGNORE INTO permission_actions (action) VALUES ('notices.view');
INSERT OR IGNORE INTO role_permissions (role, action, granted) VALUES
    ('admin', 'notices.view', 1),
    ('manager', 'notices.view', 1),
    ('super_admin', 'notices.view', 1);
