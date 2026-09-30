-- ut-docs#3281 (ADR-0129 §2, §3, slice 2b): the till persists what an
-- installed plugin declares it IS (`provides`, a closed capability set) and
-- where it is FOR (`markets`, ISO 3166-1 alpha-2) — the `plugins` table
-- stores no manifest JSON, so both need their own rows. Written by
-- PersistManifest and Rollback (replaced on every update), removed on
-- uninstall through the ON DELETE CASCADE, and read only through
-- data.PluginRepo (PluginsProviding, CapabilityProviderOwner). Only
-- Ed25519-verified, installed manifests ever reach these tables.
--
-- One row per value; the (plugin_id, value) primary key refuses a
-- duplicate the manifest parser already rejects. The value indexes serve
-- "who provides X" lookups, which are by capability, not by plugin.
--
-- Migrations are frozen the moment they merge to main (ADR-0100) — never
-- edit this file; append a new NNN_*.sql instead.
CREATE TABLE IF NOT EXISTS plugin_provides (
    plugin_id  TEXT NOT NULL,
    capability TEXT NOT NULL,
    PRIMARY KEY (plugin_id, capability),
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_plugin_provides_capability ON plugin_provides (capability);

CREATE TABLE IF NOT EXISTS plugin_markets (
    plugin_id TEXT NOT NULL,
    market    TEXT NOT NULL,
    PRIMARY KEY (plugin_id, market),
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_plugin_markets_market ON plugin_markets (market);
