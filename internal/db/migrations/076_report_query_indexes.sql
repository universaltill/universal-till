-- Performance: sargable indexes for the report/inventory hot-path
-- date-range queries the 2026-08-30 performance audit found defeating
-- their existing indexes (ut-docs#1319, universal-till/docs/code-reviews/
-- 2026-08-30-performance-audit.md section C).
--
-- The root cause, confirmed by reading windowArgs' own doc comment in
-- internal/data/pos_repo.go: sales.created_at is NOT stored in one
-- canonical text form (the schema DEFAULT is space-separated
-- datetime('now'), but every real INSERT path writes RFC3339 with a 'T'
-- and trailing 'Z' instead), so every report/inventory window query wraps
-- BOTH the stored column and the bound param in SQLite's datetime(...) to
-- normalize them before comparing. That wrapping is deliberate and correct
-- — but it makes the predicate non-sargable against a plain
-- CREATE INDEX ... (created_at) index: SQLite cannot use an index on the
-- raw column to satisfy a predicate on a *function* of that column, so
-- every one of these queries falls back to a much-less-selective index
-- (idx_sales_status) or a full scan, with cost growing with the shop's
-- entire lifetime history instead of the requested window.
--
-- Fix chosen: SQLite expression indexes on the exact predicate shape each
-- query group uses, NOT normalizing the storage format at write time —
-- deliberately, per the ticket's own "pick whichever is less invasive to
-- the write path" acceptance criterion. An expression index needs zero
-- query-code changes and zero write-path changes; it is purely additive.
-- Verified live (this repo's own sqlite3 driver, EXPLAIN QUERY PLAN,
-- 100k-row synthetic sales table, WITHOUT running ANALYZE — this product
-- never calls ANALYZE anywhere, so sqlite_stat1 does not exist on a real
-- till and the planner's default no-stats heuristics are what actually
-- matter, not the stats-informed picture ANALYZE would give): a
-- single-column expression index on bare datetime(created_at) is NOT
-- enough on its own — without stats, SQLite still prefers the existing
-- idx_sales_status equality index over it. Every wrapped query in
-- pos_repo.go also filters status = 'completed' (confirmed: all ~15
-- call sites), so a composite (status, datetime(created_at)) expression
-- index is what the planner actually picks, with any further sale_type
-- filter applied as a cheap residual filter on the already-narrowed rows.
-- The identical non-sargable-wrapped-column pattern also shows up on
-- audit_log below (fixed the same way) and on worker_allocations/payments
-- (NOT fixed here — see that section's comment for why an expression index
-- cannot be used there at all).

-- Fixes SalesByDay, PeriodComparison, RefundsByWindow, TopItems/SlowItems,
-- DeadStock, MarginByItem, SalesByDepartment/SalesByTill, busyBuckets,
-- SalesForTaxWindow, SalesForTaxBands, dateRangeSummaryInstant,
-- ItemDailySellRates and the seasonal-forecast window query — every
-- status='completed' + datetime(created_at)-range query in pos_repo.go.
-- NOT dateRangeSummary/EndOfDay(Range) (review finding, ut-docs#1319):
-- those use date(created_at, 'localtime') BETWEEN, not datetime(...) — the
-- same non-sargable 'localtime'-modifier shape as the worker_allocations/
-- payments queries below, which this composite demotes to a status-only
-- search (confirmed live: EXPLAIN QUERY PLAN shows
-- "idx_sales_status_created_dt (status=?)", the date bound reduced to a
-- residual filter). See that section's comment for why it can't be
-- indexed at all, not just why this particular index doesn't reach it.
-- idx_sales_created (001_init.sql, plain unwrapped column) is untouched
-- and still load-bearing for the cursor-based sync queries elsewhere in
-- pos_repo.go that compare created_at raw, unwrapped.
CREATE INDEX IF NOT EXISTS idx_sales_status_created_dt
    ON sales (status, datetime(created_at));

-- NOT fixed by any index in this migration, despite worker_allocations/
-- payments being named in the ticket: worker_allocation_repo.go's queries
-- use date(allocated_at, 'localtime') / date(paid_at, 'localtime'), and
-- pos_repo.go's dateRangeSummary (EndOfDay/EndOfDayRange's shared engine)
-- uses date(created_at, 'localtime') BETWEEN — SQLite classifies
-- date()/datetime()'s 'localtime' modifier as NON-deterministic
-- (it depends on the host's timezone database, which SQLite cannot
-- guarantee stable), and flatly refuses to let a non-deterministic
-- expression back a persisted index: CREATE INDEX itself succeeds (lazy
-- validation), but the very next INSERT/UPDATE against the table then
-- fails outright with "non-deterministic use of date() in an index" —
-- confirmed live against this repo's own sqlite driver while building
-- this migration, which would have broken every payment/allocation write
-- in production. A bare (non-'localtime') expression index would be
-- deterministic and buildable, but would NOT match these queries'
-- predicate shape (confirmed the same way as dateRangeSummary above), so
-- it wouldn't fix the sargability problem it needs to fix. All three need
-- a structurally different fix (e.g. a precomputed, write-time-derived
-- local-date column these queries can filter on directly) rather than a
-- matching expression index — filed as a follow-up card rather than
-- attempted here.

-- pos_repo.go's shift cash-adjustment net query filters
-- entity_type = 'shift' AND action = 'cash_adjustment' AND
-- datetime(created_at) range. idx_audit_entity (001_init.sql) covers
-- (entity_type, entity_id) — no action column, so it doesn't help this
-- query either; this is a distinct composite for the distinct predicate.
CREATE INDEX IF NOT EXISTS idx_audit_log_entity_action_created_dt
    ON audit_log (entity_type, action, datetime(created_at));

-- variant_barcodes.variant_id had no index anywhere in 75 prior
-- migrations — catalog_repo.go's variant->barcode lookups
-- (WHERE b.variant_id = v.id) run a correlated scan per variant on every
-- catalog page load/mutation. Plain equality lookup, no date wrapping.
CREATE INDEX IF NOT EXISTS idx_variant_barcodes_variant
    ON variant_barcodes (variant_id);

-- sale_links(sale_id)/sale_links(original_sale_id) were unindexed (only
-- the _archive twin's reset_batch_id was) — every /journal/{receipt}
-- detail view and refund-chain lookup in pos_repo.go does a full scan on
-- one or the other. Plain equality lookups, no date wrapping.
CREATE INDEX IF NOT EXISTS idx_sale_links_sale
    ON sale_links (sale_id);
CREATE INDEX IF NOT EXISTS idx_sale_links_original_sale
    ON sale_links (original_sale_id);

-- Deliberately NOT added here (ut-docs#1319 acceptance criteria: weigh
-- write-path cost explicitly before adding, especially for the two tables
-- below, which are checkout-hot on the write side): sale_lines.item_id /
-- sale_lines.variant_id (and their _archive twins), sales.register_id,
-- stock_movements.location_id. Correction from an earlier draft (review
-- finding, ut-docs#1319): this migration does NOT avoid the checkout
-- write path — idx_sales_status_created_dt lands on sales itself, the
-- most checkout-hot table in the schema, and audit_log is also written
-- inside CompleteSale (internal/pos/sales.go's RecordStockMovement path
-- writes one audit_log row per stock-adjusted line). The write cost was
-- measured directly instead: a 50k-row insert benchmark against `sales`
-- with idx_sales_status_created_dt present adds roughly 0.7µs per insert
-- (0.108s -> 0.140s for 50k inserts), and building the composite index
-- over a synthetic 500k-row table at upgrade time took well under a
-- second — neither is a meaningful checkout-latency or startup-stall
-- risk. sale_lines/register_id/location_id are excluded for a different,
-- stronger reason found the same way: none of the report/inventory
-- queries this card targets actually reach rows through those columns
-- (sale_lines report queries all drive from sales and reach lines via
-- the existing ux_sale_lines_sale_line(sale_id) index; register_id/
-- location_id appear only inside rare admin-side deletion-guard EXISTS
-- checks) — so those three would be pure write cost for no read win,
-- independent of how cheap that cost turned out to be. Filed as a
-- follow-up card, sequenced alongside #1318's own checkout-write-path
-- work, in case a future read pattern actually needs them.
