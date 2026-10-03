-- 064_no_sale_events.sql — universaltill/ut-docs#2558: "No sale" opens the
-- cash drawer without a sale (POST /api/pos/no-sale, internal/pages/
-- no_sale_api.go) and every such open is recorded here, so the cloud sales
-- aggregate (ADR-0111) can report no_sale_count / by_cashier.no_sale_opens
-- instead of a hard-coded 0. An open is recorded only AFTER the drawer-kick
-- bytes reached the printer — a failed kick records nothing.
--
-- Append-only historical record, same conventions as shrinkage_events
-- (035_shrinkage_events.sql):
--   - actor_id is the session user who pressed No sale; approver_id is NULL
--     unless a manager PIN elevated past the cash_adjustment permission
--     gate (internal/pages/elevation.go's checkOrElevate). Neither carries a
--     FOREIGN KEY to users(id): the event must survive a later user
--     deletion, same reasoning as audit_log.actor_id.
--   - register_id is resolved exactly like the sale row's own register_id
--     (request → this till's register identity → EnsureRegister), so a
--     no-sale lands in the same (day, till) rollup key as the sales rung up
--     on that device. Unlike shrinkage_events it carries NO FOREIGN KEY to
--     registers(id): the drawer has already opened by the time the row is
--     written, so the insert must not be refusable by a register-table
--     detail, and a historical row must not block deleting a register.
--   - till_id mirrors sales.till_id (a replica's journaled row, ADR-0011
--     D3): NULL for an open recorded on this till, and today always —
--     journaling no-sales to the main till is ut-docs#3562. The rollup's till key is
--     COALESCE(NULLIF(till_id,''), NULLIF(register_id,''), <self till>) —
--     internal/data/sales_aggregate_repo.go's tillKeyExpr, the sales rule.
--   - local_date is the local calendar day of created_at, computed at
--     insert time with date(created_at, 'localtime') — the same expression
--     InsertSale uses for sales.local_date, so the business date matches.
--   - created_at is TEXT, app-supplied (RFC 3339 UTC), so the event row and
--     its audit_log row carry identical timestamps.
--   - reason is an optional short free-text note (the handler caps it at
--     200 characters).
--
-- no_sale_events_archive is its reset-archive twin ("Clear transaction
-- history", ADR-0042 — the gap ut-docs#3452 closed for shrinkage_events):
-- column-identical plus reset_batch_id (FK to reset_batches, as 063), no
-- PRIMARY KEY, no FKs to live tables.
-- reset_archive_repo.go's resetArchiveTables carries the column list.
--
-- CREATE TABLE/INDEX IF NOT EXISTS throughout, same replay-safety
-- convention as every migration since 021.
CREATE TABLE IF NOT EXISTS no_sale_events (
    id          TEXT PRIMARY KEY,
    created_at  TEXT NOT NULL,
    local_date  TEXT NOT NULL DEFAULT '',
    register_id TEXT,
    till_id     TEXT,
    actor_id    TEXT,
    approver_id TEXT,
    reason      TEXT
);

CREATE INDEX IF NOT EXISTS idx_no_sale_events_local_date ON no_sale_events (local_date);

CREATE TABLE IF NOT EXISTS no_sale_events_archive (
    id             TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    local_date     TEXT NOT NULL DEFAULT '',
    register_id    TEXT,
    till_id        TEXT,
    actor_id       TEXT,
    approver_id    TEXT,
    reason         TEXT,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_no_sale_events_archive_batch ON no_sale_events_archive (reset_batch_id);
