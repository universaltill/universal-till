-- 108_card_readers.sql — ut-docs#6566: the per-till card reader registry.
-- One row per physical (or device) card reader THIS till can take a card
-- payment on. Till-local: never in the LAN journal, a snapshot or the cloud
-- roster (reader ids are device specific); it travels in a full backup like
-- any table. The browser only ever sees `id`; `reader_ref` is the provider's
-- own id, opaque to core. Soft delete: history, refunds and "do not
-- re-import the legacy *_reader_id setting" need the row.
CREATE TABLE IF NOT EXISTS card_readers (
  id                 TEXT PRIMARY KEY,
  name               TEXT NOT NULL,
  method_key         TEXT NOT NULL,
  plugin_id          TEXT NOT NULL,
  reader_ref         TEXT NOT NULL,
  kind               TEXT NOT NULL DEFAULT 'cloud',
  source             TEXT NOT NULL DEFAULT 'manual',
  sort_order         INTEGER NOT NULL DEFAULT 0,
  unattended_default INTEGER NOT NULL DEFAULT 0,
  last_used_at       TEXT,
  created_at         TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at         TEXT NOT NULL DEFAULT (datetime('now')),
  removed_at         TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_card_readers_ref ON card_readers(method_key, reader_ref);
CREATE INDEX IF NOT EXISTS idx_card_readers_active ON card_readers(removed_at, sort_order);
