-- ut-docs#3697 (ADR-0149 §3): the neutral plugin-records channel. A plugin
-- holding records:sync and declaring records_forward queues records on a
-- replica (plugin_record_outbox); the main till dedupes what it applied
-- (plugin_record_inbox); a plugin's small main-owned snapshot values
-- (plugin_snapshot) reach replicas through the admin pull. Nothing is
-- written unless a plugin declares records_forward.
--
-- Outbox and inbox are till-local operational bookkeeping: never part of
-- the admin bundle. plugin_snapshot is carried as its own additive
-- plugin_snapshots section of the bundle, so it needs the same
-- sync_admin_version.generation triggers the bundle's tables have
-- (migration 023), or a replica would never re-pull a changed snapshot.

CREATE TABLE IF NOT EXISTS plugin_record_outbox (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    plugin_id       TEXT NOT NULL,
    collection      TEXT NOT NULL,
    record_id       TEXT NOT NULL,
    payload         TEXT NOT NULL,
    bytes           INTEGER NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','rejected')),
    reason          TEXT NOT NULL DEFAULT '',
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    rejected_at     TEXT,
    UNIQUE (plugin_id, collection, record_id)
);

CREATE INDEX IF NOT EXISTS idx_plugin_record_outbox_due
    ON plugin_record_outbox (status, next_attempt_at, seq);

CREATE TABLE IF NOT EXISTS plugin_record_inbox (
    plugin_id    TEXT NOT NULL,
    collection   TEXT NOT NULL,
    record_id    TEXT NOT NULL,
    from_till_id TEXT NOT NULL,
    received_at  TEXT NOT NULL,
    PRIMARY KEY (plugin_id, collection, record_id)
);

CREATE INDEX IF NOT EXISTS idx_plugin_record_inbox_received
    ON plugin_record_inbox (plugin_id, received_at);

CREATE TABLE IF NOT EXISTS plugin_snapshot (
    plugin_id  TEXT NOT NULL,
    collection TEXT NOT NULL,
    payload    TEXT NOT NULL,
    version    INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (plugin_id, collection)
);

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_snapshot_ins AFTER INSERT ON plugin_snapshot
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_snapshot_upd AFTER UPDATE ON plugin_snapshot
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_snapshot_del AFTER DELETE ON plugin_snapshot
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
