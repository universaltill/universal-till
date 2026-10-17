-- 083_cloud_retired.sql — ut-docs#4522 (ADR-0162 §4): local records the
-- cloud-master catalogue pull could only retire, not delete.
--
-- A cloud tombstone for an option set that an active item still uses
-- deactivates the set (delete_option_set's rule) and drops its cloud_links
-- row — tombstones are final, so the feed never names that set again.
-- Each row here is such a set: the pull retries its delete on every chunk
-- commit and removes the row once the set is gone, so a set the item later
-- drops is not orphaned. entity is the feed's kind, cloud_id the
-- tombstoned row's id (audit only).
--
-- Local to the main till, which alone pulls; never LAN-synced
-- (sync_admin_repo.go's not-synced list), like cloud_links.
CREATE TABLE IF NOT EXISTS cloud_retired (
    entity     TEXT NOT NULL,
    local_id   TEXT NOT NULL,
    cloud_id   TEXT NOT NULL,
    retired_at TEXT NOT NULL,
    PRIMARY KEY (entity, local_id)
);
