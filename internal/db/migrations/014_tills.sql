-- Enrolled tills (ADR-0011 primary/replica sync, increment D1). This table
-- lives on the PRIMARY; each replica authenticates sync calls with its
-- bearer (only the SHA-256 is stored, like sessions).
CREATE TABLE IF NOT EXISTS tills (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    bearer_hash  TEXT NOT NULL UNIQUE,
    enrolled_at  TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen_at TEXT
);
