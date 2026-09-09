-- 018_plugin_entries_type_layout.sql — ADR-0088 Decision B (ut-docs#1904):
-- the plugin-type taxonomy gains `layout` (declarative UI structure, the
-- axis `theme` — asset-only CSS — deliberately is not), so the
-- plugin_entries.type CHECK constraint has to admit it. ADR-0002's price
-- for a new type is "ADR + code + CHECK + docs together"; this file is the
-- CHECK part, internal/plugins.CanonicalTypes the code part.
--
-- Also admits `language`: CanonicalTypes gained it after 001_init.sql was
-- baselined and the CHECK was never widened, so a language pack declaring
-- an entry of its own type failed at persist time with a raw constraint
-- error. TestPersistManifest_EveryCanonicalEntryTypeInserts (internal/
-- plugins) now proves the two lists agree at the DB level.
--
-- Why a rebuild and not an edit of 001_init.sql: verifyAppliedMigrations
-- (internal/db/db.go) hard-fails every already-migrated device on a
-- checksum drift of an applied file (see 013's header for the same trap).
-- SQLite has no ALTER TABLE for a CHECK constraint, so widening one means
-- the documented rebuild: create the replacement, copy, drop, rename,
-- recreate indexes. 003_kitchen_station_display_flag.sql rejected exactly
-- this rebuild for kitchen_stations — and its reasoning is the reason it
-- is SAFE here: that table is a PARENT (item_station_routes /
-- category_station_routes hold ON DELETE CASCADE foreign keys onto it), so
-- DROPping it inside the migration transaction with foreign_keys=ON fired
-- the cascade and silently deleted every routing rule. plugin_entries is a
-- CHILD only: it references plugins(id) and NOTHING in the schema
-- references plugin_entries (grep "REFERENCES plugin_entries" in this
-- directory finds no row, no trigger, no view — and applyMigration's
-- transaction sees no deferred violation either, since dropping child rows
-- never violates a constraint). The RENAME rewrites REFERENCES clauses in
-- other tables that point at the renamed table (SQLite ≥ 3.26 with
-- legacy_alter_table off); there are none, so nothing is rewritten. The
-- column list is copied by name, in 001's declared order, so a future
-- column added to plugin_entries by a LATER migration is unaffected (this
-- file runs before it) and one added EARLIER cannot exist (there is none).
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
        'layout'
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
