-- Idle auto-lock (docs: architecture/pos-auth.md, increment 2026-07-14):
-- track when a session was last used so stale sessions can be revoked.
ALTER TABLE sessions ADD COLUMN last_seen_at TEXT;
UPDATE sessions SET last_seen_at = created_at WHERE last_seen_at IS NULL;
