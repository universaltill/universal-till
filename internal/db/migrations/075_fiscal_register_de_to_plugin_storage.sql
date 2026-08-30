-- fiscal_register_de moves into plugin-owned KV storage (ADR-0072,
-- ut-docs#1106).
--
-- ADR-0050 Decision 1 places the §146a Abs. 4 AO till-notification
-- bookkeeping squarely in the German tax plugin: the obligation's data
-- should only exist while com.universaltill.tax-de is installed, and should
-- leave with it (plugin uninstall already wipes the plugin's plugin_storage
-- namespace). Migration 059 created it as a core table before that boundary
-- was drawn; this migration retires the table — 059 itself stays untouched,
-- append-only — by copying every row into plugin_storage as a JSON blob
-- under the plugin's id, keyed 'fiscal_register:<id>', then dropping the
-- table (DROP TABLE inside a later migration has precedent: 030).
--
-- The json_object key names below MUST match internal/data/fiscal_repo.go's
-- fiscalRegisterDERecord json tags exactly — this INSERT..SELECT is the
-- one-shot path every pre-075 row round-trips through, and a mismatched key
-- would silently drop that field on unmarshal. A NULL commissioned_on/
-- decommissioned_on becomes JSON null, which unmarshals back to the same
-- nil the old nullable columns produced.
--
-- Destroys nothing (ADR-0042, and migration 059's own discipline): every
-- row is copied before the DROP, all statements run in the migration's one
-- transaction, and the same statements run identically on a fresh install
-- (zero rows to copy) and an upgrading shop (real German till/TSE
-- bookkeeping moves across). The stock_locations address_* columns 059 also
-- added are core location data, not the plugin's, and are untouched here.
--
-- The IF NOT EXISTS shell below makes this migration replayable on its own
-- (074's convention): several upgrade tests rewind schema_migrations into
-- the 60..74 range — 059 does not replay there, so without the shell the
-- INSERT..SELECT would fail on the already-dropped table. On a real till
-- the table always still exists at this point and the shell is a no-op; on
-- a replay it recreates an empty shell that the copy reads zero rows from
-- and the DROP removes again. Column list matches 059 exactly (minus the
-- FK/index, which no row can need in an always-empty shell).
CREATE TABLE IF NOT EXISTS fiscal_register_de (
    id                    TEXT PRIMARY KEY,
    register_id           TEXT NOT NULL,
    eas_type              TEXT NOT NULL,
    eas_software          TEXT NOT NULL,
    eas_serial            TEXT NOT NULL,
    tse_serial            TEXT NOT NULL,
    tse_certification_id  TEXT NOT NULL,
    tse_type              TEXT NOT NULL,
    acquired_on           TEXT NOT NULL,
    commissioned_on       TEXT,
    decommissioned_on     TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL
);

INSERT INTO plugin_storage (plugin_id, key, value, updated_at)
SELECT 'com.universaltill.tax-de',
       'fiscal_register:' || id,
       json_object(
           'id', id,
           'register_id', register_id,
           'eas_type', eas_type,
           'eas_software', eas_software,
           'eas_serial', eas_serial,
           'tse_serial', tse_serial,
           'tse_certification_id', tse_certification_id,
           'tse_type', tse_type,
           'acquired_on', acquired_on,
           'commissioned_on', commissioned_on,
           'decommissioned_on', decommissioned_on,
           'created_at', created_at,
           'updated_at', updated_at
       ),
       updated_at
FROM fiscal_register_de;

DROP TABLE fiscal_register_de;
