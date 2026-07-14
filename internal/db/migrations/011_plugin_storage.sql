-- Per-plugin key/value storage behind the wasm `storage` host function
-- (docs: architecture/wasm-runtime.md, host functions v2). Size caps are
-- enforced in the repo layer (key 128 B, value 64 KiB, 1024 keys/plugin).
CREATE TABLE IF NOT EXISTS plugin_storage (
    plugin_id  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      BLOB NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (plugin_id, key)
);
