# Review — sync-history retention inventory + guards (ut-docs#3123)

Date: 2026-10-08 · Lane: lane:cloud-24 · Author: Opus 5.5 · Reviewer: Fable (independent subagent)

## What shipped

- **Inventory (AC 1).** Every sync-history table is already bounded by the
  code that owns it, or kept on purpose:

  | table | bound | owner |
  |---|---|---|
  | `sales_aggregate_uploads` | rows before the 14-day look-back window deleted every upload round | `cloudsync` → `POSRepo.PruneSalesAggregateUploads` |
  | `held_sales_tombstones` | 24 h TTL, pruned on every tombstone write and claim | `held_sales_repo.go` (`heldSaleTombstoneTTL`) |
  | `sync_asset_ledger` | one row per downloaded asset file; row deleted when the file is pruned | `SyncAssetLedgerRepo.Apply` via `sync_assets_prune.go` |
  | `sync_admin_version` | single row (`CHECK id = 1`) | migration 023 |
  | `sync_journal_quarantine` | **kept, never pruned**: one row per poison sale (UNIQUE sale_id) the primary could not apply — possibly its only copy (ADR-0040, ADR-0065) | shown on `/sync-quarantine` |

  Not sync history, noted for completeness: `order_status_events` (per-sale
  status journal shown on the sale; outside LAN sync), `issue_reports_sent`
  (human-rate, tiny) — no change.
- **AC 2 / AC 4:** no table needs a new 30-day prune, and pruning the
  quarantine would lose sales, so no repository method was added and
  housekeeping still never opens the database
  (`TestPackageNeverTouchesTheDatabase` unchanged).
- **AC 3:** `housekeeping.Retention()` gains a `database: <table>` row per
  sync-history table. `TestRetentionTableCoversEverySyncHistoryTable` scans
  the migrations so a new `sync_*` table fails until it declares its bound;
  `TestRetentionTableNeverListsLegalRecords` refuses any database row naming
  a legal-record table (every table with an `_archive` twin, every archive,
  `sales`/`sale_*`, `shifts`, `fiscal_*`, `audit_log`, `report_archive`, …).

No runtime behaviour change: `internal/server` only maps Kind → Policy for
`Run()` results, which never emit the new kinds. No migration, no i18n, no
manual change (the manual's clean-up list is unchanged and already says
sales and legal records are never removed).

## Findings (Fable review)

| # | sev | finding | outcome |
|---|---|---|---|
| 1 | major | legal-record check missed live tables with an `_archive` twin (`shifts`, `no_sale_events`, `stock_movements`, …) | fixed — derived from migrations + named list, self-checked both ways |
| 2 | minor | a `database:` row could be misread as permission to prune | fixed — doc comments say it documents the bound only |
| 3 | minor | review record / inventory only in Go comments | fixed — this file |
| 4 | nit | CREATE TABLE regex needed `\s+` and quoted names | fixed |
| 5 | nit | quarantine policy should say it is one row per sale | fixed |

## Verified beyond automated tests

- TDD: both new tests ran red before the retention rows existed.
- Mutation: a fake migration `CREATE TABLE\n  IF NOT EXISTS  "sync_fake_history"`
  fails the coverage test; a row pointed at `shifts` fails the legal-record
  test; both restored green.
- Each Policy/EnforcedBy claim traced to the code by the reviewer.
- `deadcode` / `golangci-lint` can't run in this container (tool built with
  Go 1.25 < repo's 1.27, fails identically on `main`); CI runs them.

Verdict: safe to merge.
