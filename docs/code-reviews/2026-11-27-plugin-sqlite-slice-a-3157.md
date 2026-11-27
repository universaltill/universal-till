# Plugin-owned SQLite, slice A (ut-docs#3157, ADR-0121 build 5)

Combined Tester + Reviewer of the service-shops batch, 2026-11-27 (Fable;
code by Sonnet). Scope: the new package `internal/db/plugindb` and
`internal/plugins/plugin_data_survives_3157_test.go`. No existing non-test
file is changed and nothing calls the package yet. The batch record is
`2026-11-27-service-shops-batch.md` (tip branch).

## What shipped

`plugindb.Manager` / `Store`: one SQLite file per plugin at
`<data>/plugin-data/<id>/plugin.db`, hardened per connection, plugin-owned
migrations with a checksum ledger, and the uninstall / restore / delete-now /
prune lifecycle. Go API only; the `db_exec` / `db_query` host functions, the
manifest fields and the install/uninstall wiring are later slices.

## ADR-0121 conformance (§2 id rule, §3 bounds, §4, §8)

| ADR text | Code | Verdict |
|---|---|---|
| Data under the sibling root, never the code tree | `plugin-data/<id>/`, `MkdirAll` 0700, file 0600, opened `mode=rw` on a file the host created | conforms; test proves a `plugin-data` tree survives code uninstall and version prune |
| Id `^[a-z0-9][a-z0-9._-]*$`, at most 128, no `..` | same pattern, checked on every entry point | conforms, and stricter: a trailing dot is refused (R5) |
| One file per plugin, WAL, one connection | one pinned `*sql.Conn`, `SetMaxOpenConns(1)` | conforms. The ADR says "driver connection hook"; a pinned connection gives the same guarantee without touching other databases in the process (a lost connection errors, it is never silently replaced by an unhardened one) |
| `SQLITE_LIMIT_ATTACHED=0`, `max_page_count` from quota, `trusted_schema=OFF`, `journal_size_limit` 4 MiB | all applied; quota default 50 MiB | conforms; TEMP schema capped too (R3) |
| ATTACH / DETACH / VACUUM / PRAGMA refused by first keyword after comments; multi-statement refused | `checkStatement` | conforms; verified `VACUUM INTO` is also stopped by the ATTACHED limit itself |
| Migrations `NNNN_name.sql`, in order, one transaction, tracked in `_ut_migrations`; downgrade refused | conforms; adds a SHA-256 per applied migration (tamper → fail closed) | conforms after R1 |
| Result at most 1 MiB; statement interrupted at deadline | `ErrResultTooLarge`; context honoured (runaway CTE test) | conforms |
| Uninstall moves data to `.removed/<id>-<date>`, kept 30 days, reinstall restores, `keep_years` refuses delete | `Uninstall` / `Restore` / `DeleteNow` / `Prune`; nothing deleted by uninstall; prune only strict-name, marked, non-symlink trees past both dates | conforms; not yet on a timer, not yet called by the uninstall handler |
| No extension loading | modernc has none; a test pins that `load_extension` and `readfile` fail | conforms |

Not in slice A (ADR task 5 remainder, tracked on #3157 and N4 #7110): host
functions and `db:own`, manifest `db.migrations` / `limits.storage_mb` /
`retention.keep_years` and the signing mirror, pre-upgrade file copy and
rollback, `db_open_import`, handover event, backups of `plugin-data`,
read-only retention mode, version-pattern-strict prune.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | high (security) | Migration SQL went to the connection unchecked. A migration could run `PRAGMA max_page_count=…` and lift the storage quota for the connection's lifetime (reproduced: 16 pages → 1,000,000), switch `trusted_schema` back on or set `writable_schema`, or `COMMIT` mid-file and break the one-transaction guarantee. Migrations are plugin-authored text; the signature proves who wrote them, not that they are harmless. | **Fixed**: `splitMigration` cuts the file into single statements (quote, comment, `CREATE TRIGGER … END` and `CASE … END` aware), refuses a statement starting with ATTACH / DETACH / VACUUM / PRAGMA / BEGIN / COMMIT / END / ROLLBACK / SAVEPOINT / RELEASE, refuses those four file-or-setting keywords as a bare word anywhere, refuses any mention of `_ut_migrations`, and runs the statements one by one in the one transaction. `TestMigrationCannotRunPragmaOrTransactionControl` (13 cases, each also checks nothing was applied and the quota is intact) and `TestMigrationSplitterRunsTriggersAndQuotedText` (a real trigger, quoted `"pragma"` / `[vacuum]` columns). |
| R2 | medium | `Exec` accepted `BEGIN`. On the single pinned connection that leaves a transaction open: every later "successful" write is uncommitted and is rolled back at `Close`, and the WAL checkpoint cannot run. | **Fixed**: transaction control is refused in `Exec` / `Query` (the host function is one statement per call). `TestTransactionControlRefusedInExec`. |
| R3 | medium | `max_page_count` covers only the main database. With `temp_store=2` a `CREATE TEMP TABLE … AS` fills host memory with no bound (reproduced: 20 MB under a 64 KiB quota). | **Fixed**: `PRAGMA temp.max_page_count` = min(quota, 8 MiB). `TestTempTablesAreCapped`. |
| R4 | low | `Restore` removed the marker before the move; a failed move left an unmarked tree that could never be restored or purged. | **Fixed**: move first, then drop the marker. |
| R5 | low | An id ending in a dot passes the ADR pattern, but Windows drops the trailing dot from a directory name, so `shop.` would open `shop`'s database. | **Fixed** in this package (fails closed, `ErrInvalidID`). The manifest validators (till and ut-cloud) should get the same rule: follow-up on #3157. |
| R6 | low | `Query` turns a BLOB into a JSON string, so non-UTF-8 bytes are replaced. | Accepted for slice A: the ABI card decides the BLOB encoding (base64) before any plugin can call it. Listed on #3157. |
| R7 | low | SQLite's defensive mode is not set (modernc exposes no `db_config`), so a plugin can read and write its own file through `sqlite_dbpage`. | Accepted: it reaches only its own file; the ledger can be damaged only by the plugin that owns it, which then fails closed on the next open. |
| R8 | note | Sorts and recursive CTEs still use heap, bounded only by the call deadline. | Accepted: the wazero memory limit and the deadline are the bound (ADR §8); to measure when the host function is wired. |
| R9 | note | `Open` holds the manager lock while migrating, and a re-`Open` ignores a changed quota. | Accepted; nothing calls it yet. Listed on #3157. |
| R10 | note | An `Open` for an id whose data sits in `.removed` creates a fresh database, after which `Restore` refuses (`ErrExists`). | By design (never overwrite); the install wiring must call `Restore` first. Listed on #3157. |

## Verification done personally

- `go test -race -count=1 ./internal/db/plugindb`: ok (31 tests).
- R1–R3 were reproduced before the fix with a probe test (quota lifted by a
  migration PRAGMA, `BEGIN` accepted, a 20 MB temp table accepted), then
  fixed; the new tests pass.
- `VACUUM INTO` after a migration `COMMIT` was probed before the fix: the
  ATTACHED limit refused it and no file was written.
- Full `go test -timeout 90m -p 3 ./...` on this branch rebased on main
  (`d25ce05da`): every package `ok` (`internal/pages` 1185 s,
  `internal/plugins` 1852 s) except one wall-clock assertion in
  `internal/data`
  (`TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks`:
  246 ms against a 100 ms budget) while the machine's load average was above
  140. Re-run alone it passes (3.4 s); the package is untouched by this
  change.
- `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0
  issues), `shellcheck`, licences check, and all 74 guards named in
  `ci.yml`'s build job: pass, except `guard-armv7-seccomp.sh`, which needs
  `qemu-arm` (not installed on this Mac; CI has it).
- Recurring bugs: every write is preceded by `os.MkdirAll`; no cwd-relative
  path (the root is passed in, `paths.Data("plugin-data")` by the caller).

Pre-flight: no user-visible behaviour, locale key or help topic (nothing is
wired); no new host function or manifest field yet (those slices update
ADR-0002 mirrors and `plugin-host-functions.md`); no ADR change (conforms to
ADR-0121); review record this file; follow-ups on #3157 and #7110.

Verdict: ship slice A.
