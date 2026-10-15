-- 078_guest_order_results.sql — ut-docs#4337 (ADR-0159 §4/§5): the result
-- of each guest_order directive this (main) till applied, keyed by the
-- cloud's GuestOrder id.
--
-- Two jobs, one small table:
--   * idempotency: a re-delivered directive gets this stored result back
--     (same display_no and total) without a second apply;
--   * delivery: a result the cloud did not acknowledge (network error, or a
--     non-2xx other than 404) stays posted = 0 and is re-posted on each
--     later check-in until the cloud answers 200 or 404 — so an order the
--     till parked is never shown to the guest as lost.
--
-- Money is integer minor units (total_minor) with its ISO 4217 currency.
-- Bounded: GuestOrderResultsRepo.Prune drops posted rows after a day and
-- unposted ones after two (the cloud deletes a GuestOrder 24 h after it
-- turns terminal, so a later post could only 404). Per-till bookkeeping,
-- never synced (sync_admin_repo.go's not-synced list).
CREATE TABLE IF NOT EXISTS guest_order_results (
    guest_order_id TEXT PRIMARY KEY,
    directive_id   TEXT NOT NULL,
    status         TEXT NOT NULL,
    message        TEXT NOT NULL DEFAULT '',
    display_no     TEXT NOT NULL DEFAULT '',
    total_minor    INTEGER NOT NULL DEFAULT 0,
    currency       TEXT NOT NULL DEFAULT '',
    posted         INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_guest_order_results_posted ON guest_order_results(posted, created_at);
