-- Archived generated reports (docs: architecture/end-of-day-report.md).
-- One row per report instance; kind+period unique so the end-of-day job
-- is naturally idempotent ("today's report already exists").
CREATE TABLE IF NOT EXISTS report_archive (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,             -- 'eod' (weekly/monthly later)
    period       TEXT NOT NULL,             -- e.g. '2026-07-14'
    content_json TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (kind, period)
);
