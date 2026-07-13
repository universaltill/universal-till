-- Operator sessions for PIN login (docs: architecture/pos-auth.md).
-- The cookie value is an opaque random token; only its SHA-256 is stored.
CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    token_hash  TEXT NOT NULL UNIQUE,
    user_id     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at  TEXT NOT NULL,
    revoked_at  TEXT,
    FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
