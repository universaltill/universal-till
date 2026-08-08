-- "My reports" tracking (universaltill/ut-docs#348): once a bug-report
-- bundle (ADR-0022) uploads successfully, cloudsync used to Discard the
-- whole thing — note, media, everything — leaving the manager no trace of
-- what they reported or what became of it. This table retains the cheap
-- part (note + attachment summary) while the bulky media blobs are still
-- discarded, and tracks the cloud-side status pulled down on the sync tick.
--
-- id is the till's own bundle id (issuereport.Meta.ID) — the correlation
-- key the cloud echoes back in GET /v1/stores/issue-reports, so status
-- pulls match rows without any server-assigned identifier.
--
-- status is 'sent' locally until the cloud confirms one of its own states
-- (received/transcribing/ready/filed/discarded); an offline till simply
-- keeps showing the last-known value. Timestamps are RFC3339 TEXT, same as
-- every other table here.
CREATE TABLE issue_reports_sent (
    id               TEXT PRIMARY KEY,
    note             TEXT NOT NULL DEFAULT '',
    captured_at      TEXT NOT NULL,
    had_audio        INTEGER NOT NULL DEFAULT 0,
    had_video        INTEGER NOT NULL DEFAULT 0,
    image_count      INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT 'sent',
    github_issue_url TEXT NOT NULL DEFAULT '',
    last_synced_at   TEXT
);

CREATE INDEX idx_issue_reports_sent_captured_at ON issue_reports_sent(captured_at);
