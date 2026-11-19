# Review: a shop switch never swaps the database under a running till (ut-docs#6672)

Date: 2026-11-19. Branch `test/6672-shop-switch-db-guard`. Author: Sonnet (Dev). Reviewer: Opus 5.5 (independent, fresh context).
Tester: PASS (Sonnet, independent of the author). Own mutations (file replaced by a fresh sqlite, watch disabled; `settings` dropped; `users` dropped) each turn the switch tests red, also on the committed 5-test version. `go test ./internal/enroll -race` ok, `go test ./internal/db` ok, gofmt/vet/golangci-lint clean, ci.yml guards pass bar four env-only failures (catimport tags not fetched, go1.27 toolchain, shellcheck binary, qemu-arm) that CI runs. No driven app run: test-only change, no UI.
Pre-flight: review record — this file · help topic / docs-shots n/a (test-only, no user-visible change) · `reference/` n/a (no behaviour change; the "a shop switch only writes settings rows, a file swap happens only at boot" note goes on the #6672 close comment, BA AC 5) · README n/a · en.json/i18n n/a (no strings) · ADR n/a (no new decision; pair clears identity before the call per ADR-0116 D5, unchanged) · migrations n/a.

## What shipped
One new test file, `internal/enroll/shop_switch_db_test.go`. No production change. Each test opens a real migrated
database the way `app.Run` does (`testsupport.MigratedDBFile` in `t.TempDir()`, `db.AcquireDataDirLock`, `db.Open`,
`db.NewFileWatch`) and backs the enroll settings with `data.SettingsRepo` on that handle. `assertDBIntact` then requires
`FileWatch.Check() == nil` (same inode for the DB and lock file), plus `SELECT COUNT(*)` on `settings` and `users`
through the pool after `SetMaxIdleConns(0)` and through a separate `sql.Open(... mode=rw)` connection.
- `TestShopSwitchPair_KeepsTheDatabaseFile`: a till in shop A pairs to a new shop (fake cloud 200).
- `TestShopSwitchPair_FailedKeepsTheDatabaseFile`: the cloud answers 500. The cloud must have been called.
- `TestShopSwitchMove_KeepsTheDatabaseFile`: a pending move is approved through `DecideMove`, with the real
  `ResetSalesAggregateUploads` hook on the same DB.
- `TestShopSwitchMove_FailedKeepsTheDatabaseFile` (added in review): the cloud refuses the approval (410 `move_expired`).
- `TestShopSwitchGuard_DetectsADeletedFile`: shows the watch can fail.

## Findings
| id | sev | finding | outcome |
|---|---|---|---|
| R1 | minor | BA AC 3 asks that a failed **move** leave a working DB with "nothing wiped first". Only a failed pair was covered. | fixed: `TestShopSwitchMove_FailedKeepsTheDatabaseFile` asserts a typed `MoveRefusedError` from a real cloud call, `store_id`/`token` unchanged in the DB, the ledger hook never called, and `assertDBIntact` |
| R2 | nit | `TestShopSwitchGuard_DetectsADeletedFile` overlaps `internal/db` `TestFileWatch_*` | kept: it proves the guard as wired in *this* file can fail |
| R3 | nit | the "same handle keeps serving" `repo.Get` after `assertDBIntact` adds little, because the pool queries already cover it | kept, harmless |

Checked, no defect found:
- **False pass.** The pool query is a genuinely new connection, not a pooled one. With the watch check skipped and the file
  replaced by an empty one, all four switch tests fail with `pool query on settings: no such table: settings`. A
  reused old connection would still have seen the unlinked inode's tables. With a *valid migrated* DB renamed over the
  path, the table queries alone would pass, and all four tests fail on `FileWatch.Check` ("deleted or replaced"). Both
  layers bite, and the watch covers the case the queries cannot. The Dev's own red-proof (delete → watch error, and
  delete with no watch → `unable to open database file`) is consistent with this.
- **Exercised paths.** The failed pair checks `cloud.calls > 0`. The move tests reach `DecideMove`, and the store id is
  asserted in the DB. A generic `Pair` error would make `DecideMove` fail, so the loose `err == nil` check on the
  pending move cannot hide a wrong path.
- **Package globals.** `bootDBTill` calls `resetState()` and re-registers it as cleanup. That resets `cur`, the flags,
  pending moves and the hooks (`resetMoves`). Cleanups run LIFO: hooks reset → `initForTest`'s cancel + `wg.Wait()`
  (the background loop stops before the DB closes) → `resetState` → `db.Close` → lock release → httptest close.
  No test calls `t.Parallel()` (none in the package do), so the globals are safe. `-race -count=3 -shuffle=on` passes.
- **Hygiene.** `t.TempDir()` only, through `MigratedDBFile`. The only date is `2026-01-01T00:00:00Z`, outside the
  60-day window. There are no mail addresses, and the shop data is the package's generic fixtures. Raw SQL appears only
  in `_test.go`, which `guard-data-access.sh` exempts. Windows CI never runs `internal/enroll`, so
  `os.Remove` on an open file in the guard test is Linux/macOS only, which is fine.
- **BA central claim, re-verified on HEAD `4625675b`.** `afdc0def` (#5049) is an ancestor: `mode=rw` is in
  `internal/db/db.go:130`, and there are `FileWatch` and `TestOpen_DeletedFileDoesNotRecreatePhantomDB`. `internal/enroll`
  has no `os.Rename`/`os.Remove` and no DB close. `resetIdentityForPair`, `adoptPaired` and `adoptMoved` only write
  settings rows. `ApplyPendingRestore` and `RecoverInterruptedRestore` have one caller, `internal/app/app.go:153/180`,
  before `db.Open`. The remaining DB-path removals are boot-time (`paths.go:93`, the legacy copy) or side files (WAL/SHM,
  the swap and restore notes). No shop-switch path swaps the file while the server runs.

## Verification
- `go test ./internal/enroll/ -count=1`: ok. `go test -race -count=1 ./internal/enroll/` (the `test-heavy` leg's scope): ok.
  `-race -count=3 -shuffle=on -run ShopSwitch`: ok.
- `gofmt -l internal/enroll` is empty, `go vet` is ok, `golangci-lint run ./internal/enroll/` gives 0 issues.
- `guard-fixed-dates.sh`, `guard-mail-addresses.sh` and `guard-data-access.sh` pass.
- The mutations above were run inline and restored byte-identical (`cmp`).

## Deferred
- BA AC 6 (manual Mac check: pair the running Mac till to another shop, the log has no `no such table`, it sells
  straight after) needs the owner's Mac. It goes on the #6672 close comment, not in this PR.

Verdict: approve after fixes (R1 fixed in review).
