# Perf: sargable indexes for report/inventory date-range queries (ut-docs#1319)

**Card:** ut-docs#1319 — principal-engineer performance audit finding, 2026-08-30
(`docs/code-reviews/2026-08-30-performance-audit.md` section C). **Priority:** p1.
**Complexity:** medium. **Dev:** Sonnet (inline). **Review:** Opus (one round,
worktree-isolated, findings fixed — no second round needed, none were blocker-class).

## What shipped

One append-only migration, `internal/db/migrations/076_report_query_indexes.sql`,
adding indexes only — no query code changed, no write-path behaviour changed.

**Root cause:** `sales.created_at` is stored in two different text formats
(schema `DEFAULT (datetime('now'))` vs. every real INSERT path's RFC3339), so
~15 report/inventory queries in `internal/data/pos_repo.go` wrap both the
column and the bound param in `datetime(...)` to normalize before comparing.
That wrapping is correct, but it makes the predicate non-sargable against a
plain `created_at` index — SQLite can't use an index on a raw column to
satisfy a predicate on a *function* of that column, so every one of these
queries fell back to the much-less-selective `idx_sales_status` index (or a
full scan), with cost growing with the shop's entire lifetime history.

**Fix chosen:** SQLite expression indexes matching each query group's exact
predicate shape, not normalizing the storage format at write time (the
ticket's own "pick whichever is less invasive to the write path" acceptance
criterion) — purely additive, zero query-code or write-path changes for the
tables actually indexed:

- `idx_sales_status_created_dt ON sales (status, datetime(created_at))` —
  the headline fix. Every wrapped query also filters `status = 'completed'`,
  and (confirmed live, see below) a bare single-column `datetime(created_at)`
  index is NOT enough on a real till: this product never runs `ANALYZE`
  anywhere, so `sqlite_stat1` never exists in production, and without stats
  SQLite's planner still prefers the existing status-equality index over a
  single-column expression index. Only the composite wins under real
  no-`ANALYZE` conditions. Fixes SalesByDay, PeriodComparison,
  RefundsByWindow, TopItems/SlowItems, DeadStock, MarginByItem,
  SalesByDepartment/SalesByTill, busyBuckets, SalesForTaxWindow,
  SalesForTaxBands, `dateRangeSummaryInstant`, ItemDailySellRates, and the
  seasonal-forecast window query.
- `idx_audit_log_entity_action_created_dt ON audit_log (entity_type, action, datetime(created_at))`
  — fixes the shift cash-adjustment net query (existing `idx_audit_entity`
  has no `action` column, so it didn't help either).
- `idx_variant_barcodes_variant`, `idx_sale_links_sale`,
  `idx_sale_links_original_sale` — three plain equality indexes on columns
  that had none anywhere in 75 prior migrations (catalog variant→barcode
  lookups, `/journal/{receipt}` detail views).

**Deliberately NOT fixed, and why (this matters — read before assuming a gap
here is an oversight):**

- `worker_allocations.allocated_at` / `payments.paid_at` (`date(col,
  'localtime')`), and `dateRangeSummary`/`EndOfDay(Range)`'s
  `date(created_at, 'localtime')` — SQLite classifies the `'localtime'`
  modifier as **non-deterministic** (depends on the host's timezone
  database) and refuses to let a non-deterministic expression back a
  persisted index. `CREATE INDEX` itself succeeds (lazy validation), but the
  very next `INSERT`/`UPDATE` against the table then fails outright with
  `non-deterministic use of date() in an index` — confirmed live, and it
  would have broken every payment/allocation write in production if shipped.
  A bare non-`'localtime'` expression index would be buildable but wouldn't
  match these queries' predicate shape, so it wouldn't fix anything either.
  These need a structurally different fix (e.g. a precomputed,
  write-time-derived local-date column) — **filed as a follow-up card**,
  not attempted here.
- `sale_lines.item_id`/`variant_id` (+ `_archive` twins), `sales.register_id`,
  `stock_movements.location_id` — named in the ticket, excluded after
  investigation showed none of this card's target queries actually reach
  rows through those columns (sale_lines report queries all drive from
  `sales` and reach lines via the existing
  `ux_sale_lines_sale_line(sale_id)`; `register_id`/`location_id` appear
  only inside rare admin-side deletion-guard `EXISTS` checks) — pure write
  cost for no read win, independent of how cheap that cost is. Filed as a
  follow-up card alongside #1318's checkout-write-path work, in case a
  future read pattern changes that.

## Independent review (Opus, worktree-isolated)

**Verdict: SAFE WITH FIXES.** Both load-bearing SQLite claims (composite
needed without `ANALYZE`; `'localtime'` unindexable) were independently
re-derived with throwaway `sqlite3` scripts, not taken on faith. Query plans
for 13 real production query shapes were replayed before/after against
migrations 001–075 vs. 001–076, confirming every claimed fix is real and the
sync-cursor query using raw unwrapped `created_at` is unaffected.

Four findings, all fixed, none blocker-class:

1. **Doc-accuracy bug** — the migration's own "Fixes ..." list wrongly
   included `dateRangeSummary`/`EndOfDay(Range)`; they use the same
   unindexable `'localtime'` shape as worker_allocations/payments and stay
   unfixed. Corrected the comment and added
   `TestMigration076DateRangeSummaryStaysUnfixed` as a regression guard so a
   future accidental fix (or accidental claim of one) doesn't go unnoticed.
2. **Doc-accuracy bug** — an earlier draft claimed this migration "only
   touches read-heavy admin-page tables, none...on the CompleteSale hot
   path," which is backwards on both counts (`sales` is the headline index
   and the most checkout-hot table in the schema; `audit_log` is written
   inside `CompleteSale` via `RecordStockMovement`). Replaced with the
   actual measured write cost (~0.7µs added per `sales` insert; well under a
   second to build the composite index over a synthetic 500k-row table at
   upgrade) and the real reason the other three columns are excluded
   (unreached by these queries, not "avoiding the hot path").
3. Dead code in the test file — a second `if` branch that could never fire
   because the first `if` already `t.Fatal`s on the same condition. Removed.
4. Missing `rows.Err()` check in one test's scan loop, inconsistent with its
   sibling loop in the same file. Added.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | clean / pass / pass |
| `go test ./internal/db/... -run TestMigration076 -v` | 6 tests / 8 subtests, all pass |
| `go test ./...` (full suite) | all packages green, zero failures |
| `guard-migration-version-collision.sh` / `guard-data-access.sh` | pass / pass |
| Real query plans, 13 production query shapes, before/after | every claimed fix confirmed real (see review findings above) |
| Both SQLite claims (composite-without-ANALYZE; 'localtime' unindexable) | independently reproduced twice — once during Dev, once during Review |
| Write-path cost on `sales`/`audit_log` | measured directly (~0.7µs/insert; sub-second index build at 500k rows) |
| No real shop/client name, no secret-shaped literal | confirmed — generic test IDs only |
| UX guidelines / help-topic checklist | N/A — backend-only, no user-facing surface |

## Deferred (new Backlog cards, not this PR)

- `worker_allocations.allocated_at` / `payments.paid_at` / `dateRangeSummary`'s
  `date(col, 'localtime')` predicates need a structurally different fix
  (e.g. a precomputed local-date column) — SQLite cannot index the
  `'localtime'` modifier at all.
- `sale_lines`/`sales.register_id`/`stock_movements.location_id` indexes,
  sequenced with #1318's checkout-write-path work if a future read pattern
  needs them.

## Safe to merge

Yes. Purely additive migration (new indexes only, `IF NOT EXISTS`, no
existing migration file touched), zero query-code or write-path behaviour
change for any table actually indexed, full test suite green, independent
review's findings all fixed and re-verified.
