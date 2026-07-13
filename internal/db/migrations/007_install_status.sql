-- Marketplace install-status records move out of the settings KV store into
-- their own table (they are installer bookkeeping, not user settings).
CREATE TABLE IF NOT EXISTS plugin_install_status (
    listing_id      TEXT PRIMARY KEY,
    plugin_id       TEXT,
    plugin_name     TEXT,
    target_version  TEXT,
    current_version TEXT,
    state           TEXT NOT NULL,
    message_key     TEXT,
    retryable       INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL
);

-- Carry over existing records stored as JSON under
-- settings key 'plugins.marketplace.install_status.<listing-id>'.
INSERT OR IGNORE INTO plugin_install_status
    (listing_id, plugin_id, plugin_name, target_version, current_version,
     state, message_key, retryable, updated_at)
SELECT
    json_extract(value, '$.listing_id'),
    json_extract(value, '$.plugin_id'),
    json_extract(value, '$.plugin_name'),
    json_extract(value, '$.target_version'),
    json_extract(value, '$.current_version'),
    COALESCE(json_extract(value, '$.state'), 'active'),
    json_extract(value, '$.message_key'),
    COALESCE(json_extract(value, '$.retryable'), 0),
    COALESCE(json_extract(value, '$.updated_at'), datetime('now'))
FROM settings
WHERE key LIKE 'plugins.marketplace.install_status.%'
  AND json_extract(value, '$.listing_id') IS NOT NULL;

DELETE FROM settings WHERE key LIKE 'plugins.marketplace.install_status.%';
