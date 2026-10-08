-- 068_plugin_schedules.sql — ut-docs#3161 (ADR-0121 §8, build card 9a): the
-- till persists an installed plugin's `schedules[]` — periodic ticks the host
-- raises as ordinary events to that plugin alone — because the `plugins`
-- table stores no manifest JSON. Written by PersistManifest and Rollback
-- (replaced on every install/update/rollback), removed on uninstall through
-- the ON DELETE CASCADE, and read only through data.PluginRepo
-- (ListPluginSchedules) by the wasm runtime's schedule ticker. Only
-- Ed25519-verified, installed manifests ever reach this table, and the
-- manifest parser has already enforced every_s >= 30, 0 <= jitter_s <=
-- every_s and the `<plugin-id>.` event namespace; the CHECKs below are a
-- second, schema-level floor, not the primary validation.
--
-- One row per (plugin, event): the primary key refuses a duplicate event.
-- Till-local, never synced (sync_admin_repo.go nonAdminTables): a replica
-- re-installs the plugin from its own verified manifest.
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
CREATE TABLE IF NOT EXISTS plugin_schedules (
    plugin_id TEXT    NOT NULL,
    event     TEXT    NOT NULL,
    every_s   INTEGER NOT NULL CHECK (every_s >= 30),
    jitter_s  INTEGER NOT NULL DEFAULT 0 CHECK (jitter_s >= 0 AND jitter_s <= every_s),
    PRIMARY KEY (plugin_id, event),
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE
);
