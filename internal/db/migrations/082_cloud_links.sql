-- 082_cloud_links.sql — ut-docs#4463 (ADR-0162 §4): the cloud-master
-- catalogue pull's bookkeeping on the main till.
--
-- cloud_links maps a cloud catalogue row (entity = the feed's kind, cloud_id
-- = its UUID) to the local record it became, the same shape and rules as
-- import_source_links (081). version is the feed version last applied; gen
-- is the resync generation that last saw the row — after a 410 resync's
-- full pull, links with an older gen are pruned through the delete rules.
-- No foreign key onto the catalogue: the entity is generic and a link whose
-- local record is gone is re-pointed on the next apply.
--
-- cloud_apply_failures keeps the latest refusal per cloud row (a row the
-- till's own validation refused keeps the till's previous version; the
-- cursor still moves past it) until a later version of that row applies.
--
-- Both are local to the main till, which alone pulls; never LAN-synced
-- (sync_admin_repo.go's not-synced list) — additional tills get the
-- resulting catalogue rows through normal sync.
CREATE TABLE IF NOT EXISTS cloud_links (
    entity     TEXT    NOT NULL,
    cloud_id   TEXT    NOT NULL,
    local_id   TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    gen        INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT    NOT NULL,
    PRIMARY KEY (entity, cloud_id)
);

CREATE INDEX IF NOT EXISTS idx_cloud_links_local
    ON cloud_links (entity, local_id);

CREATE TABLE IF NOT EXISTS cloud_apply_failures (
    entity   TEXT    NOT NULL,
    cloud_id TEXT    NOT NULL,
    version  INTEGER NOT NULL,
    reason   TEXT    NOT NULL,
    at       TEXT    NOT NULL,
    PRIMARY KEY (entity, cloud_id)
);
