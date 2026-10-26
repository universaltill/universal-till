# Review: sales copy storage and change log, shipped dark (ut-docs#5074 M1)

Part of ut-docs#5074 (design ut-docs `reference/design-5074-mirror-sales-to-replicas.md`,
ADR-0011 / ADR-0114 "Amendment 2026-10-26", contract
`reference/contracts/pos-lan-sales-mirror.md`).

## What shipped

- Migration `093_sales_mirror.sql` (pinned): the read-only copy tables
  (`mirror_sales` + six child tables, `mirror_closes`, `mirror_state`), the
  main till's change log (`sales_mirror_log`, `sales_mirror_acks`), three
  change-log triggers on `sales` (insert, update of the copied columns) and
  `report_archive` (insert), and a trigger that drops a till's ack when its
  `tills` row is deleted (revoke, cloud tombstone).
- **Off = inert, decided: the triggers are installed but are no-ops while
  `sales_mirror.enabled` is not `1`** (every till today: the key has no
  row). Not installing them at enable time was rejected: it needs runtime
  DDL outside the migration ledger (ADR-0100) and a second schema state
  the backup-restore schema allowlist (`schema_allowlist.go`) would have
  to know. The gate also excludes a replica (`sync.primary_url`) and a till
  with its own kill switch (`sync.sales_mirror_off`).
- `internal/data/sales_mirror_repo.go`: the settings keys and bounds only.
  Its methods arrive with their first caller (M2, M3) — the deadcode guard
  refuses unreachable functions.
- `scripts/ci/guard-sales-mirror-isolation.sh` (+ sandboxed self-test, in
  `ci.yml`): only `internal/data/sales_mirror_repo*.go` may name a copy or
  change-log table, case-insensitively, in Go and in any `.sql` outside the
  migrations. Two reviewed exceptions: the admin-bundle table classification
  (`nonAdminTables`) and the join-snapshot reset.
- The join snapshot no longer carries `mirror_state`, `sales_mirror_log` or
  `sales_mirror_acks` (a promoted main till's copy cursor or another till's
  acks must not travel); the copied rows stay.
- `sales_mirror.` classified shop-wide; the 11 tables classified out of the
  admin bundle; `mirror_payments.masked_pan` allowlisted (ADR-0127, design D4).
- Harness `internal/pages/sales_mirror_harness_test.go`: one shop, the main
  till serving its real sync mux over TLS (httptest), additional tills A/B
  as real migrated databases with a joined till's identity, real
  `pos.CompleteSale` sales and the real journal push.

## Proof that off is inert

- `TestSalesMirrorLog_OffWritesNothing`: sales, a void, a provenance stamp,
  a kitchen status write and a Z archive with the setting absent and `0`:
  every one of the 11 new tables stays empty.
- `TestSalesMirrorLog_OffEqualsNoTriggers`: the same workload on a database
  with the triggers dropped leaves **every table** with the same row count.
- `TestMigration093_SalesMirror_SafeOnExistingData`: migrating a DB that
  already holds sales, a void and a close changes no row and logs nothing.
- The whole `go test ./...` (every sale, Z-report, sync, journal and
  backup suite) runs on the migrated schema with the feature off: green.
- TDD check: removing the `sales_mirror.enabled` term from the insert
  trigger makes both off tests fail (`sales_mirror_log has 6 rows with the
  sales mirror off`; `3 rows with the triggers (off) vs 0 without`);
  restored, they pass.

## Other tests

- On: one pointer per insert, void, provenance stamp and close, none for a
  kitchen status write (`TestSalesMirrorLog_OnLogsEachChange`); replica and
  kill switch log nothing.
- Same transaction: an injected abort on the sale's lines or payments rolls
  back the header and the log row (`TestSalesMirrorLog_RollsBackWithTheSale`)
  — this also pins that every child of a sale is written in
  `CompleteSale`'s transaction.
- AC3: 10,000 copied sales change none of this till's Z figures, sales
  aggregate, next receipt number, push queue, stock, vouchers or sales rows.
- Harness: A's and B's journaled sales are logged on M with origin A / B,
  M's own with origin `''`; A and B log nothing; off, M logs nothing.
- Join snapshot: bookkeeping reset on the copy only, live DB untouched.

## Trigger overhead on a sale (feature on)

`BenchmarkCompleteSale_SalesMirror`, real migrated DB on disk, Apple M2 Max,
3000 sales × 3 runs: no triggers 1.40–1.45 ms, off 1.47–1.49 ms, on
1.49–1.54 ms per `CompleteSale` (the commit's fsync dominates). The
reviewer's independent run (800 × 3): 0.80–0.88 / 0.79–0.85 / 0.80–0.81 ms.
Both put the trigger cost within noise. Pi 5 measurement is M8's.

## Independent review (Fable, strict)

Verdict: safe to merge dark. Findings:

1. major — a record with duplicate child ids would hard-fail the apply and
   wedge the copy (repo apply, not shipped in M1): **fixed in M3** (duplicate
   ids rejected, per-record savepoint).
2. minor — guard case-sensitive, `.sql` outside migrations not scanned:
   **fixed** (`grep -i`, `*.sql` scanned; self-test cases added).
3. minor — `PruneLog` with an empty epoch prunes everything: **fixed in M2**
   (refused).
4. minor — record seq not checked against the page range: **fixed in M3**.
5. minor — AC3 isolation test missing: **added** here.
6. nit — migration comment named a non-existent test file: **fixed**.
7. nit — "cheapest test first" was not true: **fixed** (the in-row status
   test now comes first in the sale triggers; comment corrected).
8. nit — after the join-snapshot reset `sqlite_sequence` keeps the old head:
   recorded for M6 (log position on promote).
9. nit — unparsable state values read as 0; harmless.

Reviewer's independent checks: WHEN short-circuit and `INSERT OR REPLACE`
trigger behaviour verified in a scratch SQLite program; reset-archive moves
sales by `INSERT … SELECT` + `DELETE` (no delete trigger, nothing logged
while off); a backup taken before or after 093 still restores
(`checkMigratedSchema` compares against this build's own schema).

## Deferred

- The repository methods, feed, pull loop: M2 / M3. M6 must set the
  promoted till's log position explicitly (finding 8).
