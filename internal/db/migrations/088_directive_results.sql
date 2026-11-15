-- 088_directive_results.sql — ut-docs#4797: the result of each generic cloud
-- directive this till applied (adjust_stock, set_price, rename_item, …),
-- keyed by the cloud's directive id.
--
-- Idempotency: when the result post fails, the cloud keeps the directive
-- pending and delivers it again. Most hooks are not idempotent (a stock
-- adjustment or receipt books a second movement), so a directive id found
-- here is NOT applied again: its stored first result is re-posted instead.
-- posted = 1 once the cloud acknowledged it (200, or 404: no longer pending).
--
-- Bounded: DirectiveResultsRepo.Prune drops posted rows after 7 days and
-- unposted ones after 30 (the cloud stops delivering an acknowledged
-- directive; 30 days bounds one it never acknowledges). Per-till
-- bookkeeping, never synced (sync_admin_repo.go's not-synced list).
CREATE TABLE IF NOT EXISTS directive_results (
    directive_id   TEXT PRIMARY KEY,
    directive_type TEXT NOT NULL,
    status         TEXT NOT NULL,
    message        TEXT NOT NULL DEFAULT '',
    posted         INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_directive_results_posted ON directive_results(posted, created_at);
