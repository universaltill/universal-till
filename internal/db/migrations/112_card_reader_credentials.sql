-- 112_card_reader_credentials.sql — ut-docs#6538 (ADR-0198 §6.2/6.3): the
-- identity of a paired phone card reader, one row per reader, written in the
-- same transaction as its card_readers row. Till-local: never in the LAN
-- journal, a snapshot, the admin bundle or the cloud roster (the join snapshot
-- empties it); it travels in a full backup only.
--
-- Only the SHA-256 of the 32-byte bearer is stored (the plaintext is shown to
-- the phone once, over pinned TLS). No card data, no cloud credential.
--   pin_spki       the LAN TLS pin this main served when the reader was
--                  approved; the reader link is accepted only on it.
--   next_pin       the pin this till will serve after a planned key change.
--   revoked_at     set the moment a manager removes the reader.
--   bearer_prev_*  the previous bearer, honoured until its grace ends after
--                  a rotation (a lost reply must not lock the phone out).
CREATE TABLE IF NOT EXISTS card_reader_credentials (
  reader_id               TEXT PRIMARY KEY REFERENCES card_readers(id),
  bearer_hash             TEXT NOT NULL UNIQUE,
  bearer_prev_hash        TEXT,
  bearer_prev_expires_at  TEXT,
  pin_spki                TEXT NOT NULL DEFAULT '',
  next_pin                TEXT NOT NULL DEFAULT '',
  revoked_at              TEXT,
  last_seen_at            TEXT,
  last_ip                 TEXT NOT NULL DEFAULT '',
  created_at              TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_card_reader_credentials_prev ON card_reader_credentials(bearer_prev_hash);
