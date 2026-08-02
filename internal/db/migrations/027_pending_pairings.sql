-- Approve-to-pair (ADR-0033 part 2/3, universaltill/ut-docs#184): a
-- replica's pair request sits here until a manager approves or denies it.
-- Only the SHA-256 commitment of the replica's request_secret is stored,
-- never the secret itself (mirrors tills.bearer_hash). token is populated
-- on approve and only ever released to a caller who proves possession of
-- the secret (SHA-256(request_secret) == commitment).
CREATE TABLE pending_pairings (
    id           TEXT PRIMARY KEY,
    device_name  TEXT NOT NULL,
    commitment   TEXT NOT NULL,
    token        TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending'
);

CREATE INDEX idx_pending_pairings_status ON pending_pairings(status);
CREATE INDEX idx_pending_pairings_expires_at ON pending_pairings(expires_at);
