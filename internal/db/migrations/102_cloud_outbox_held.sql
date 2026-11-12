-- ut-docs#6088: catalogue edits the cloud cannot take yet (fields outside the cloud's advertised
-- cloud-catalog-changes set). Not in cloud_outbox, so the uploader never rescans them each tick;
-- released back into cloud_outbox when the accepted set changes. Main-till bookkeeping, never synced.
CREATE TABLE IF NOT EXISTS cloud_outbox_held (
    entity     TEXT NOT NULL,
    local_id   TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (entity, local_id)
);
