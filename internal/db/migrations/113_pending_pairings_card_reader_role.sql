-- 113_pending_pairings_card_reader_role.sql — ut-docs#6538 (ADR-0198 §6.2).
-- A pairing request may now ask to join as a card reader. SQLite cannot alter
-- a CHECK, so the (ephemeral, till-local, FK-free) request table is rebuilt.
CREATE TABLE pending_pairings_new (
    id             TEXT PRIMARY KEY,
    device_name    TEXT NOT NULL,
    commitment     TEXT NOT NULL,
    token          TEXT NOT NULL DEFAULT '',
    requested_at   TEXT NOT NULL,
    expires_at     TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    requested_role TEXT NOT NULL DEFAULT 'additional' CHECK (requested_role IN ('additional', 'satellite', 'card_reader')),
    served_pin     TEXT NOT NULL DEFAULT ''
);
INSERT INTO pending_pairings_new (id, device_name, commitment, token, requested_at, expires_at, status, requested_role, served_pin)
  SELECT id, device_name, commitment, token, requested_at, expires_at, status, requested_role, served_pin FROM pending_pairings;
DROP TABLE pending_pairings;
ALTER TABLE pending_pairings_new RENAME TO pending_pairings;
CREATE INDEX idx_pending_pairings_status ON pending_pairings(status);
CREATE INDEX idx_pending_pairings_expires_at ON pending_pairings(expires_at);
