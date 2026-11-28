# Plugin-owned SQLite, slice B (ut-docs#3157 + #7110, ADR-0121 build 5)

Combined Reviewer of the slice B batch, 2026-11-28 (Fable; independent of the
Dev and Tester agents). Scope: the whole uncommitted diff of branch
`feat/3157-plugin-sqlite-slice-b` against `origin/main` (`163537a7e`): 26
changed files and 35 new ones. Slice A's record is
`2026-11-27-plugin-sqlite-slice-a-3157.md`.

Pre-flight: `en.json` keys → `en.meta.json` updated (4 keys, by the Tester);
host functions → `reference/plugin-host-functions.md` and the SDK binding
(`sdk/plugin`, `guard-sdk-hostfns.sh` green) updated; amended ADR-0121 →
amendment 2026-11-28 on the ut-docs branch, reworded by this review (R2, R3,
R6); handover contract `reference/contracts/plugin-data-handover.md` updated
by this review (R6); `web/help/` n/a (no page: the delete-now surface is a
JSON API, its button is a UX follow-up card); manifest fields / ADR-0002
taxonomy / signing mirror n/a (no new field, ABI stays 2); README n/a.

## What shipped

- **Engine** (`internal/db/plugindb`): JSON argument decoding with base64
  BLOBs, a semaphore in place of the mutex (`ErrBusy` after 5 s instead of a
  hang), per-id locks, lazy open without migrating, quota re-apply, idle close
  and a 16-store LRU, symlink refusal at every level, Windows device names in
  the id rule, statement limits (1 MiB value, 64 KiB SQL, 64 variables),
  `SnapshotFile` (`VACUUM INTO`), `MigrateForInstall` / `RestorePreUpgrade` /
  `CheckCompatible` / `SweepPreUpgrade`, retention markers with the reason
  key, and a read-only `ImportDB`.
- **Host functions** `db_exec`, `db_query`, `db_error`, `db_open_import`,
  `db_query_h` (ABI 2), the Go guest SDK binding and its fake host.
- **Install / upgrade**: migrations run from the digest-verified artifact
  before the new version activates, with an undo for a later failed step.
- **Rollback**: data brought to what the target can read; strict-semver
  version prune.
- **Uninstall**: the data tree moves to `.removed`, `keep_years` honoured;
  `POST /api/plugins/{id}/data/delete` and `GET …/data/status`; a daily
  housekeeping purge.
- **Backup / restore**: every `plugin.db` (consistent copy) and blob inside
  the snapshot, chunked; restore at boot before plugins load.
- **Handover**: a background runner raising `plugin.data.handover.ask`, with
  a per-till `plugin_handover` row (migration 122).

## Security review (what was attacked, and the result)

| Attack | Result |
|---|---|
| Open another plugin's database | Not possible: the store is chosen from `hostState.pluginID` only; the guest passes no id and no path. Mutation-tested by the Tester (store opened for another id: killed). |
| Path traversal through the id | `validatePluginID` (till) and `plugindb.validID` at every join (Open, uninstall, restore, prune, delete, backup restore); `..`, separators, trailing dot, Windows device names refused. |
| Blob names / restore archive | Restore accepts only `<id>/plugin.db` and `<id>/blobs/<name>` with both regexes, no `\`, `:`, leading `/`, control bytes; a prefix check on the resolved root; `.removed` and `.pre-upgrade` can never be written from an archive. |
| Symlinks, TOCTOU | Root resolved once; `<id>`, `.removed`, `.pre-upgrade`, `blobs` must be real directories (Lstat); `plugin.db`, `-wal`, `-shm` must be regular; after create, `SameFile(Lstat, fstat)`. A plugin has no way to create a link (blob names are validated, the DB cannot write files), so the remaining window needs local file access, which already owns the data. |
| `ATTACH` / `DETACH` | `SQLITE_LIMIT_ATTACHED=0` on the one pinned connection (the real guard), plus the first-keyword refusal. |
| `VACUUM INTO`, `PRAGMA`, transaction control, multi-statement | Refused by first keyword and by the second-statement scan (modernc would run every statement of a string, so that scan is load-bearing: its quoting rules were compared with SQLite's four quote styles and both comment forms). |
| `PRAGMA` through table-valued functions | Probed: `pragma_max_page_count`, `pragma_trusted_schema`, `pragma_writable_schema`, `pragma_synchronous`, `pragma_journal_mode`, `pragma_temp_store` are readable but take no value (`too many arguments` / `unknown database`); nothing can be changed. `pragma_database_list` leaked the host path: R7. |
| `writable_schema`, `sqlite_master` writes | `table sqlite_master may not be modified`; the PRAGMA is unreachable. |
| `load_extension`, `readfile`, `writefile`, `sqlite_dbpage`, `fts3_tokenizer` | Refused as text fragments anywhere (statement, migration, trigger or view body); modernc has no extension loading. |
| Trigger / view bodies | SQLite's grammar allows only INSERT/UPDATE/DELETE/SELECT there; the fragment and `_ut_migrations` checks cover the whole text; `trusted_schema=0`. |
| Import handle | Path only from `importFiles.Path(callerID, handle)`; header check; `mode=ro&immutable=1`, `query_only`, `trusted_schema=0`, ATTACH limit 0; no write function exists; at most 2 per event, closed when the event ends. |
| Handover impersonation | The event sits under the core root `plugin`: a plugin id may not start with it and a plugin may not raise it. The payload is built by core; the answer is read as two booleans. |
| Delete route | `plugin_management` permission, id validated, `confirm=yes`, audited, refused (409) inside `keep_years`; both new routes are in the demo "always denied" map. |
| Migration SQL | Loaded only from an artifact whose archive hash matched the signed manifest (unsigned import only in the explicit devplugins path); same statement guard as runtime. |

No way was found for a plugin to reach another plugin's file, the core
database, or a path outside its own directory.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | high (ADR-0121 §8, slice A R10) | A reinstall did not restore the kept data. Nothing in the install path called `Manager.Restore`; `MigrateForInstall` created an empty database, after which `Restore` refuses for good (`ErrExists`). A shop that uninstalled and reinstalled a plugin saw its history gone (it sat in `.removed` until purged). | **Fixed**: `restoreRemovedDataForInstall` runs before the migration step on a fresh install (also for a plugin with blobs and no db block), never on an upgrade, never over live data. `TestReinstallRestoresKeptData_3157`, `TestReinstallRestoreOnlyOnFreshInstall_3157`. |
| R2 | high (data loss) | Every rollback restored the pre-upgrade copy when one existed, even when the newer version had changed no schema. A rollback three weeks after an upgrade silently replaced the live database with the three-week-old copy; every row written since moved to a side file no code reads. | **Fixed**: `prepareDataForRollback` checks compatibility first and keeps the live database when the target can read it; the copy is restored only when the database is ahead (or its ledger differs), and with no copy the rollback is refused as before. `TestRollbackKeepsLiveDataWhenTargetCanReadIt_3157`; the four existing cases still pass. |
| R3 | medium (retention) | `SweepPreUpgrade` deleted `rolled-back-*.db` after 30 days. After a real data rollback that file is the only copy of the rows the newer version wrote, including records of a `keep_years` plugin. | **Fixed**: rolled-back copies are never swept; they leave only with the plugin's data as a whole (uninstall, then the retention purge). Test expectation updated in `TestSweepPreUpgradeTmpAgeAndKeepNewestTwo`. |
| R4 | medium (concurrency) | `Manager.Open`'s lock-free fast path could hand out a store that `CloseIdle` (every `Sync`) or an LRU eviction closed in the same instant; the statement then failed with `ErrClosed` (-3 to the guest), possibly a sale-path write. | **Fixed**: the fast path marks the store used, and the host functions reopen once on `ErrClosed` (it comes from `acquire`, before the statement starts, so a write can never run twice). `TestRetryClosedStoreReopensOnce_3157`. |
| R5 | low | `RestorePreUpgrade` and both `Undo`s deleted `plugin.db` before copying the replacement in. A crash in between left no database; the next lazy open would create an empty one. | **Fixed**: `replaceDBFile` removes only `-wal`/`-shm` (the file is closed and checkpointed) and swaps the main file with one rename. `TestReplaceDBFileKeepsTheCurrentFileWhenTheCopyFails_3157`. |
| R6 | medium | Five failed handover attempts abandoned the handover on that till for ever: a plugin release that fixed its handler was never asked again, so the core tables could never be dropped without hand-editing the till's database. | **Fixed**: attempts and backoff belong to the asked plugin version (`asked_version`, added to migration 122, which has not merged yet; pin updated); an upgrade is asked again from zero. `TestHandoverIsAskedAgainAfterAnUpgrade_3157`. |
| R7 | low (information) | `SELECT file FROM pragma_database_list` returned the host's absolute data path (directory layout, OS user name) to the guest. | **Fixed**: refused as a fragment. `TestHostFilePathIsNotReadableByThePlugin_3157`. |
| R8 | low (test) | `TestPerformanceBudgets` asserted one 100 ms wall-clock sample; it failed 2 runs in 5 under `-race` at load average 55 (140 to 160 ms). | **Fixed**: best of five samples, same budget. |
| R16 | medium (CI) | `guard-data-access.sh` was red (new; the Tester's table lists it green, measured before the wasm test guest was added): `internal/plugins/testdata/db_guest/main.go`, a non-test Go file, held nine query strings. CI's build job would have failed. | **Fixed**: the statements moved to `dbGuestStatements` in `wasm_db_realguest_test.go` and reach the guest in the event payload; the guard itself is unchanged. |
| R9 | **condition** (durability) | `synchronous=NORMAL` in WAL mode never corrupts the file, but a commit the plugin was told succeeded can be lost on a power cut (tills do get unplugged). For settings, caches, tabs and loyalty counters that is an acceptable price for 5 to 30 ms per write on an SD card. For a fiscal journal it is not: the receipt is printed, the plugin's record of it is gone, and core (which keeps its own durability) and the plugin disagree. | **Open, must be closed before any plugin with `retention.keep_years` (or a fiscal role) stores records through `db:own`**: open such a plugin's store with `synchronous=FULL`. Recommended rule: `keep_years > 0` implies FULL, with no manifest field and no ABI change; the alternative is an explicit `db.durability` field (needs the ut-cloud mirror). No shipped plugin uses `db:own` yet, so nothing is at risk today. Architect / owner decision. |
| R10 | medium | Backups carry live plugin data only. `.removed/<id>-<date>` (data under `keep_years` after uninstall) and `.pre-upgrade/rolled-back-*.db` are in no backup: a dead disk loses records the shop must keep. | Open: follow-up card (back up retained `.removed` trees, restore them to `.removed`). |
| R11 | medium | `temp_store=file` moves sorter and CTE spill from host memory to SQLite temp files. They are not covered by `temp.max_page_count` or the quota, only by the 3 s / 10 s statement cap; where the temp directory is a tmpfs (common on a Pi) the spill is RAM again. | Open: follow-up card (point SQLite's temp directory at the data volume and set a per-connection heap/sorter limit; measure on the Pi). |
| R12 | low | After a backup restore the plugin database can be older than the installed plugin's migrations; nothing migrates at boot (runtime opens with `NoMigrate`), so the plugin runs against an old schema until its next upgrade. | Open: follow-up card (re-run `MigrateForInstall` for installed plugins after a restore). |
| R13 | low | An event still running while its plugin is uninstalled can recreate an empty `plugin-data/<id>/` after the move; a later reinstall then keeps that empty tree (`ErrExists`). The kept data stays safe in `.removed`. | Open: follow-up card (unload before the move, or treat an empty live tree as absent). |
| R14 | low | The retention period is read from the manifests in the code tree; if the tree is unreadable the result is 0 years (fails open). `DeleteNow` acts on the newest removed tree only. The ADR's "read-only and exportable" mode for retained data is not built (design: follow-up). | Open: on the read-only-mode card. |
| R15 | low | Import files are hostile input: `cell_size_check` and `mmap_size=0` are not set. A handover handler answering `progress` for ever is asked once a minute for ever. "module not loaded" is matched as text. The SDK sends a `uint64` above the int64 range as a lossy float. A rollback to a target with more migrations than the database applies none. An install undo drops rows the old version wrote in the seconds between migrate and the failed step. | Open, one hardening card. |

Checked and found sound: no deadlock (lock order is always id lock → store
semaphore → `removedMu`; `Manager.mu` only guards the maps and is never held
across I/O); `lockIDCtx` drops its reference on a cancelled wait; `claim` /
`closeClaimed` leave a closed store reporting `ErrClosed`, never a nil
dereference; import handles and stores are closed on event end, unload,
reload and shutdown; the DSN pragmas are in effect on modernc (probed:
`journal_mode=wal`, `synchronous=1`, `temp_store=0`, `trusted_schema=0`,
`max_page_count` from the quota); a migration failure rolls its transaction
back and leaves the database byte-identical; a downgrade without a
pre-upgrade copy is refused; the prune touches only strict
`<id>-YYYYMMDD[.n]` real directories with a valid marker past both dates; a
snapshot whose plugin part failed still returns the core backup path to its
three callers, which keep it.

## Verification done personally

- `go build ./...`, `go vet` on the touched packages, `gofmt -l .`: clean.
- Before any change: `go test -race -count=1 ./internal/db/plugindb` ok;
  `go test -race -run 'TestDB|Handover|Rollback|PluginData|DataDelete|QueryCap|Uninstall' ./internal/plugins`
  ok (597 s); `go test -race -run 'PluginData|PluginBackup' ./internal/db` ok;
  `internal/pages` `TestDemo|DataDelete|PluginData` ok; `internal/housekeeping`
  ok; `sdk/plugin` ok. The Dev and Tester claims hold.
- A probe test (not kept) against a real hardened store for the PRAGMA
  table-valued functions, `sqlite_master` writes and the loaded module list.
- Each fix R1 to R5 and R7 was reverted, its test seen to fail with the
  expected message, and restored (R6: the pre-fix code has no column to ask
  again with; the new test fails at the sixth-attempt step by construction).
- After the fixes: the same targeted set plus the new tests, `-race`;
  `TestShippedMigrationsUnchanged` (pin for 122 updated); `internal/data`
  handover tests; `golangci-lint` on db, plugins, data (0 issues); guards
  data-access (green after R16), deadcode-baseline, migration-version-
  collision, fixed-dates, core-neutral, sdk-hostfns: green.
- The full suite was not re-run (the Tester ran it: about 16 minutes); it
  must run once more in the batch gate because of R1, R2, R4, R6 and R16.

## Verdict

**APPROVED WITH CONDITIONS.** The isolation holds; the data-loss and
conformance defects found (R1 to R8, R16) are fixed with tests.

Conditions:

1. R9: decide and build `synchronous=FULL` for `keep_years` / fiscal plugins
   before the first such plugin stores records through `db:own` (blocks the
   fiscal move cards, not this merge).
2. Run the full batch gate once on the reviewed tree before the push.
3. The reworded ADR-0121 amendment (points 2 and 3) and the handover contract
   on the ut-docs branch merge together with this change.

## Deferred (cards on #3157)

R10 retained data in backups; R11 temp spill bound; R12 migrate after
restore; R13 uninstall race; R14 read-only retention mode; R15 hardening
list; the "delete data now" button (UX); the ut-cloud manifest mirror of the
Windows-name id rule; `blobDir` symlink check.
