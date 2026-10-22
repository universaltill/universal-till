-- 089_plugin_entries_type_channel.sql — ADR-0169 (ut-docs#4187, #4759): the
-- plugin-type taxonomy gains `channel` (the 23rd type: the cloud-hosted
-- adapter of a sales channel), so the plugin_entries.type CHECK constraint
-- has to admit it. ADR-0002's price for a new type is "ADR + code + CHECK +
-- docs together"; this file is the CHECK part, internal/plugins.
-- CanonicalTypes the code part. TestPersistManifest_EveryCanonicalEntryTypeInserts
-- proves the two lists agree at the DB level.
--
-- A rebuild, exactly as 018_plugin_entries_type_layout.sql did for `layout`
-- (SQLite has no ALTER for a CHECK, and an edit of an applied file would
-- fail verifyAppliedMigrations on every installed till, ADR-0100). It is
-- SAFE for the reason 018 gives: plugin_entries is a CHILD table only (it
-- references plugins(id); nothing references it, no trigger or view names
-- it — migrations 019 to 087 do not touch it), so DROP TABLE cascades into
-- nothing and RENAME has no REFERENCES clauses elsewhere to rewrite. 018's
-- column list is complete: no later migration added a column.
CREATE TABLE plugin_entries_new (
    id              TEXT PRIMARY KEY,
    plugin_id       TEXT NOT NULL,

    type            TEXT NOT NULL,
    key             TEXT NOT NULL,
    label           TEXT NOT NULL,

    icon_path       TEXT,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    is_active       INTEGER NOT NULL DEFAULT 1,

    parent_page_key TEXT,
    menu_group      TEXT,
    route           TEXT,
    target_action   TEXT,
    trigger_event   TEXT,

    config_json     TEXT,

    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),

    FOREIGN KEY (plugin_id) REFERENCES plugins(id) ON DELETE CASCADE,
    UNIQUE (plugin_id, key),

    CHECK (type IN (
        'page',
        'button',
        'popup',
        'payment',
        'device',
        'integration',
        'report',
        'pricing',
        'tax',
        'import',
        'export',
        'hardware',
        'background_job',
        'scheduler',
        'receipt_template',
        'customer_facing',
        'auth',
        'notification',
        'delivery',
        'theme',
        'language',
        'layout',
        'channel'
    ))
);

INSERT INTO plugin_entries_new (
    id, plugin_id, type, key, label, icon_path, sort_order, is_active,
    parent_page_key, menu_group, route, target_action, trigger_event,
    config_json, created_at, updated_at
)
SELECT
    id, plugin_id, type, key, label, icon_path, sort_order, is_active,
    parent_page_key, menu_group, route, target_action, trigger_event,
    config_json, created_at, updated_at
FROM plugin_entries;

DROP TABLE plugin_entries;

ALTER TABLE plugin_entries_new RENAME TO plugin_entries;

CREATE INDEX idx_plugin_entries_plugin       ON plugin_entries (plugin_id);

CREATE INDEX idx_plugin_entries_type         ON plugin_entries (type);

CREATE INDEX idx_plugin_entries_parent_page  ON plugin_entries (parent_page_key);
