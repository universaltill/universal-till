-- ut-docs#1342: sargable local-calendar-day filtering for report queries that
-- use date(col, 'localtime') — SQLite classifies the 'localtime' modifier as
-- non-deterministic (depends on the host's timezone database) and refuses to
-- back a persisted index with it: CREATE INDEX succeeds (lazy validation),
-- but the very next INSERT/UPDATE against the table then fails outright with
-- "non-deterministic use of date() in an index". Confirmed live while
-- building ut-docs#1319 (docs/code-reviews/2026-08-30-report-query-indexes-1319.md).
--
-- Fix: a precomputed, write-time-derived local-date column per affected
-- table. Populated by the Go write path (internal/data's InsertSale,
-- InsertPayment, InsertWorkerAllocation, UpdateSaleStatus, SetSaleProvenance),
-- each of which computes it with `date(?, 'localtime')` bound to the same
-- created_at/allocated_at/paid_at/voided_at parameter already being written
-- — same conversion the old read-side queries applied, just relocated to
-- write time. NOT a trigger: this repo's migration runner (internal/db/db.go,
-- execMigrationStatements/splitStatements) does not split CREATE TRIGGER
-- ... BEGIN ... END blocks — splitStatements' own doc comment states
-- plainly that no migration has ever used one and the splitter would need
-- to learn the construct first. Verified empirically while drafting this
-- migration (a trigger-based first draft failed every migration test with
-- "SQL logic error: incomplete input" — the runner split the trigger body's
-- internal semicolons as if they were statement boundaries). Teaching the
-- splitter BEGIN...END is a bigger, riskier change to shared infrastructure
-- than this one card's scope, so the write-time-column approach was used
-- instead — see internal/data's own write-path methods and their test
-- fixtures for where the population actually happens.
--
-- sales.local_date: the calendar day of created_at, in the shop's local
-- timezone — serves dateRangeSummary's completed-sales totals, the
-- payments-join methods/tips queries (via sales.local_date, since those
-- filter on the sale's created_at, not the payment's paid_at), and the
-- per-till breakdown query.
--
-- sales.voided_local_date: the calendar day of voided_at (NULL until voided)
-- — serves dateRangeSummary's cancellations query, which deliberately
-- windows on voided_at, not created_at (a sale completed one day and voided
-- the next belongs, as a Storno, to the day it was cancelled — see that
-- query's own doc comment in pos_repo.go). Kept as a second column rather
-- than overwriting local_date, because the two queries in the same function
-- window on two different events.
--
-- worker_allocations.local_date: the calendar day of allocated_at — serves
-- WorkerAllocationsSummary's allocated-side query and ListWorkerAllocations.
--
-- payments.local_date: the calendar day of paid_at — serves
-- WorkerAllocationsSummary's "tip" received-side query (joins payments,
-- filters on paid_at, not on the sale's created_at).
--
-- sales_archive/payments_archive/worker_allocations_archive carry the same
-- columns (review finding, ut-docs#1342): the reset/restore round-trip
-- (internal/data/reset_archive_repo.go, resetArchiveTables) copies explicit
-- column lists between a live table and its _archive twin, and a column
-- missing on the archive side is silently dropped on restore rather than
-- erroring — the exact bug class migrations 055 (held_sales_archive.table_id)
-- and 056 (tracking_token) were already caught adding here. Without these,
-- a Settings → Data → Clear/Restore cycle would restore every sale with
-- local_date back at the column DEFAULT '' and silently zero every rewritten
-- report for that data.

ALTER TABLE sales ADD COLUMN local_date TEXT NOT NULL DEFAULT '';
ALTER TABLE sales ADD COLUMN voided_local_date TEXT;
ALTER TABLE sales_archive ADD COLUMN local_date TEXT NOT NULL DEFAULT '';
ALTER TABLE sales_archive ADD COLUMN voided_local_date TEXT;

ALTER TABLE worker_allocations ADD COLUMN local_date TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_allocations_archive ADD COLUMN local_date TEXT NOT NULL DEFAULT '';

ALTER TABLE payments ADD COLUMN local_date TEXT NOT NULL DEFAULT '';
ALTER TABLE payments_archive ADD COLUMN local_date TEXT NOT NULL DEFAULT '';

-- Backfill existing rows (one-time, plain DML — not an index expression, so
-- the 'localtime' non-determinism restriction does not apply here).
-- COALESCE(..., '') guards a malformed/unparseable source timestamp: date()
-- returns NULL for those (review finding, ut-docs#1342), and local_date is
-- NOT NULL, so an ungated UPDATE would abort this migration — and therefore
-- fail Open() and stop the till from starting — on the first bad row. A
-- malformed created_at is not hypothetical: internal/pages/sync_sales.go's
-- own history (ut-docs#647) documents one arriving from a peer's journal
-- before that fix landed, and such a row can still exist on an unmigrated
-- till today. Falling back to '' here matches the same "row still exists,
-- just doesn't match any date filter" degradation InsertSale/InsertPayment's
-- own date(?, 'localtime') calls would produce for the same bad input.
UPDATE sales SET local_date = COALESCE(date(created_at, 'localtime'), '');
UPDATE sales SET voided_local_date = COALESCE(date(voided_at, 'localtime'), '') WHERE voided_at IS NOT NULL;
UPDATE sales_archive SET local_date = COALESCE(date(created_at, 'localtime'), '');
UPDATE sales_archive SET voided_local_date = COALESCE(date(voided_at, 'localtime'), '') WHERE voided_at IS NOT NULL;

UPDATE worker_allocations SET local_date = COALESCE(date(allocated_at, 'localtime'), '');
UPDATE worker_allocations_archive SET local_date = COALESCE(date(allocated_at, 'localtime'), '');

UPDATE payments SET local_date = COALESCE(date(paid_at, 'localtime'), '');
UPDATE payments_archive SET local_date = COALESCE(date(paid_at, 'localtime'), '');

-- Composite indexes, not single-column expression indexes — this product
-- never runs ANALYZE in production (no sqlite_stat1), and #1319 confirmed
-- live that without planner stats a single-column expression index loses to
-- an existing equality index; a composite covering the equality filter too
-- wins unconditionally. sales.status is that equality filter everywhere
-- these columns are read. No indexes on the _archive twins: nothing reports
-- against archived data by date range today.
CREATE INDEX idx_sales_status_local_date ON sales (status, local_date);
CREATE INDEX idx_sales_status_voided_local_date ON sales (status, voided_local_date);
CREATE INDEX idx_worker_allocations_source_local_date ON worker_allocations (source_type, local_date);
CREATE INDEX idx_payments_local_date ON payments (local_date);
