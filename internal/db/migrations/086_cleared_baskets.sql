-- 086_cleared_baskets.sql — universaltill/ut-docs#1122: a live cashier
-- basket with at least one priced or "add by hand" line that is cleared with
-- New sale (POST /api/pos/reset, internal/pages/pos_api.go) WITHOUT a tender
-- and WITHOUT being re-parked (a resumed held order goes back on hold,
-- ut-docs#3582, and is not recorded here) used to leave no trace at all.
-- Every such clear is recorded here so the Z report can show how many
-- baskets — and how much money — were cleared before payment
-- (data.EODReport.ClearedBasketCount/ClearedBasketTotal). Never part of
-- Gross/Net/Refunds/Cancellations: no sale happened. The self-order kiosk's
-- basket, a held order's explicit Cancel and single-line voids are not
-- recorded here.
--
-- Append-only historical record, same conventions as no_sale_events
-- (064_no_sale_events.sql):
--   - actor_id is the session user who pressed New sale. No FOREIGN KEY to
--     users(id): the event must survive a later user deletion.
--   - register_id is resolved like no_sale_events.register_id (this till's
--     register identity → EnsureRegister), with no FOREIGN KEY to
--     registers(id): recording is best-effort and must never be refusable
--     by a register-table detail, and a historical row must not block
--     deleting a register.
--   - till_id mirrors sales.till_id / no_sale_events.till_id: NULL for a
--     clear recorded on this till, and today always — journaling cleared
--     baskets to the main till is a follow-up (like ut-docs#3562).
--   - local_date is the local calendar day of created_at, computed at
--     insert time with date(created_at, 'localtime') — the same expression
--     InsertSale uses for sales.local_date — and indexed so the Z report's
--     local_date BETWEEN window is sargable.
--   - created_at is TEXT, app-supplied (RFC 3339 UTC).
--   - line_count is the basket's priced lines plus its "add by hand" lines;
--     total_minor is the basket total in minor units at the moment of
--     clearing.
--
-- cleared_baskets_archive is its reset-archive twin ("Clear transaction
-- history", ADR-0042): column-identical plus reset_batch_id (FK to
-- reset_batches, as 064), no PRIMARY KEY, no FKs to live tables.
-- reset_archive_repo.go's resetArchiveTables carries the column list.
--
-- CREATE TABLE/INDEX IF NOT EXISTS throughout, same replay-safety
-- convention as every migration since 021.
CREATE TABLE IF NOT EXISTS cleared_baskets (
    id          TEXT PRIMARY KEY,
    created_at  TEXT NOT NULL,
    local_date  TEXT NOT NULL DEFAULT '',
    register_id TEXT,
    till_id     TEXT,
    actor_id    TEXT,
    line_count  INTEGER NOT NULL DEFAULT 0,
    total_minor INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_cleared_baskets_local_date ON cleared_baskets (local_date);

CREATE TABLE IF NOT EXISTS cleared_baskets_archive (
    id             TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    local_date     TEXT NOT NULL DEFAULT '',
    register_id    TEXT,
    till_id        TEXT,
    actor_id       TEXT,
    line_count     INTEGER NOT NULL DEFAULT 0,
    total_minor    INTEGER NOT NULL DEFAULT 0,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX IF NOT EXISTS idx_cleared_baskets_archive_batch ON cleared_baskets_archive (reset_batch_id);
