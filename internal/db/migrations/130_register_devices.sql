-- 130_register_devices.sql: ut-docs#7098 (epic #7097). One cloud device = one
-- register for life. A side table, NOT columns on registers: registers is an
-- admin table (synced primary-wins to every replica), and the device link is
-- main-till-local (ADR-0086), so it must never travel. The join snapshot
-- empties it (internal/db/join_snapshot.go). Numbered 130 to leave 122-129
-- free for the open PRs that claim 120/121.
CREATE TABLE IF NOT EXISTS register_devices (
    register_id    TEXT PRIMARY KEY REFERENCES registers (id),
    device_id      TEXT NOT NULL DEFAULT '',      -- '' = unknown (never auto-retired)
    till_id        TEXT NOT NULL DEFAULT '',      -- tills.id at link time, '' for none
    source         TEXT NOT NULL DEFAULT 'enrol', -- enrol | rename | backfill
    superseded_by  TEXT NOT NULL DEFAULT '',      -- device id that took this register over
    retired_reason TEXT NOT NULL DEFAULT '',      -- revoked | old_entry | stale_auto | manual | replaced
    retired_at     TEXT,
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Backfill from the latest till_enrolled audit row per till. INSERT OR IGNORE:
-- a register that is gone, or a device id already linked, is skipped. A till
-- row with no device id links with '' (unknown). The self register is not a
-- joined till's register and gets no row.
INSERT OR IGNORE INTO register_devices (register_id, device_id, till_id, source)
SELECT x.reg_id, COALESCE(t.device_id, ''), x.till_id, 'backfill'
FROM (
    SELECT a.entity_id AS till_id,
           CASE WHEN json_valid(a.data_json) THEN json_extract(a.data_json, '$.register_id') END AS reg_id
    FROM audit_log a
    WHERE a.entity_type = 'till' AND a.action = 'till_enrolled'
      AND a.rowid = (SELECT a2.rowid FROM audit_log a2
                     WHERE a2.entity_type = 'till' AND a2.entity_id = a.entity_id AND a2.action = 'till_enrolled'
                     ORDER BY a2.created_at DESC, a2.rowid DESC LIMIT 1)
) x
LEFT JOIN tills t ON t.id = x.till_id
WHERE x.reg_id IS NOT NULL
  AND x.reg_id IN (SELECT id FROM registers)
  AND x.reg_id NOT IN (SELECT value FROM settings WHERE key = 'sync.till_register_id');

CREATE UNIQUE INDEX IF NOT EXISTS idx_register_devices_device ON register_devices (device_id) WHERE device_id <> '';
CREATE INDEX IF NOT EXISTS idx_register_devices_till ON register_devices (till_id) WHERE till_id <> '';
