-- 081_import_source_links.sql — ut-docs#4429 (ADR-0161 §5): which local
-- record a foreign (source, entity, external_id) became, so an API-source
-- import plugin's re-run updates the item it created instead of
-- duplicating or skipping it. source is the importing plugin's id, stamped
-- by core (never read from a row).
--
-- No foreign key onto items: the entity is generic (customers come later,
-- ut-docs#4437), and a link whose local record was deleted is re-pointed on
-- the next run, not cascaded away. Local to the main till, where imports
-- are applied; never LAN-synced (sync_admin_repo.go's not-synced list) —
-- additional tills get the resulting catalogue rows through normal sync.
CREATE TABLE IF NOT EXISTS import_source_links (
    source      TEXT NOT NULL,
    entity      TEXT NOT NULL,
    external_id TEXT NOT NULL,
    local_id    TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (source, entity, external_id)
);

CREATE INDEX IF NOT EXISTS idx_import_source_links_local
    ON import_source_links (source, entity, local_id);
