-- Plugin data handover flag (ut-docs#3157, ADR-0121 amendment 2026-11-28).
-- Core asks a plugin whose manifest hooks `plugin.data.handover.ask` to move
-- its own data out of core tables into its own plugin.db, once per till. One
-- row per plugin: `done_at` is set when the plugin answered {"done":true};
-- until then core retries (at most 5 attempts per plugin version, backoff via
-- next_attempt_at; `asked_version` is the version those attempts belong to, so
-- an upgraded plugin is asked again from zero).
-- Deliberately no foreign key to plugins: the flag must survive an
-- uninstall/reinstall of a plugin whose data is retained, so a finished
-- handover is never asked twice. No core table is dropped by this migration.
CREATE TABLE IF NOT EXISTS plugin_handover (
    plugin_id       TEXT PRIMARY KEY,
    done_at         TEXT,
    asked_version   TEXT,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TEXT,
    next_attempt_at TEXT,
    last_error      TEXT
);
