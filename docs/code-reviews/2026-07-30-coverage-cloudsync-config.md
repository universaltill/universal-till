# Batch 10: `internal/cloudsync` + `internal/config` test coverage

**Date**: 2026-07-30
**PR**: `test/coverage-batch10-cloudsync-config`
**Coverage**: `internal/cloudsync` 59.4% → **95.3%**; `internal/config` 59.6% → **100.0%**

## What shipped

Continuation of the ongoing coverage push (batches 1–9), picking the
next-lowest real packages per the queue item's own tracking note.

### `internal/cloudsync`

- `Start()` — the till's background sync loop — was previously **0%
  covered**: no test had ever driven it at all. New tests cover the full
  lifecycle: ticks on its own interval, stops cleanly on `ctx` cancellation
  from both the pre-first-tick wait and the main loop, and survives a
  `Tick()` failure without dying.
- `apply()`'s directive-dispatch — many branches were untested despite a
  large existing fixture test: every "hook is nil" branch for `set_setting`/
  `install_plugin`, "hook present but the required id/field is blank" for
  `set_price`/`adjust_stock`/`rename_item`/`deactivate_item`/`add_barcode`,
  a hook returning an error, a hook returning an empty message (must
  default to `"done"`), string-form vs. missing-entirely numeric JSON
  payload values, and `remove_plugin` — never exercised as a directive type
  at all before this batch.
- `pushSync`/`pushSnapshotIfChanged`/`post` — DB-size reporting, a non-200
  `/v1/stores/sync` response, a malformed JSON response, `ListItems`/
  `ItemBarcodes` repository errors, and the catalog-snapshot upload itself
  failing.
- **A real gap proven, not a bug**: `pushSnapshotIfChanged`'s stock-quantity
  path (`ListStockLevels` populating the snapshot's `qty` field) had never
  actually succeeded in any prior test — the shared
  `testsupport.NewCatalogTestDB()` minimal schema has no `stock_locations`
  table, which the repository query `LEFT JOIN`s, so the query silently
  errored every time (the error is swallowed by design — best-effort sync)
  and `qty` was always `0`. No test ever caught this because nothing
  asserted a non-zero quantity. New test uses a real, fully-migrated DB
  (`internal/db.Open`) instead of the minimal schema — the tester skill's
  own rule for exactly this shape — and proves real stock quantities now
  flow through correctly. Not a production bug: the code was always
  correct; the guarantee had simply never been verified for real.
- `uploadIssueReport`/`attachFile`/`uploadPendingIssueReports` (the
  ADR-0022 issue-report upload path) — previously 0%/0%/30%. New tests
  cover the full multipart request shape (fields, audio, optional video,
  log lines, `created_at`), the "not registered" and non-200 branches, a
  missing-audio-file error, and the real `Pending → upload → Discard`
  cycle (a bundle that uploads successfully is discarded; one the cloud
  rejects stays pending for retry).

### `internal/config`

- `Init()`/`getenv()` had **zero direct coverage** — every field was only
  ever exercised indirectly through whatever a real process's ambient
  environment happened to be. New tests cover every field's default and
  every field's env-var override, plus the one nested-fallback detail
  (`UT_MARKETPLACE_STORE_ID` defaults onto `UT_STORE_NAME`'s *resolved*
  value, not a separate hardcoded literal).
- `SetOverlays`/`Available` (both live-called — `internal/plugins.go`'s
  `SetOverlays` call, `internal/httpx`/`translations_page.go`'s `Available`
  call) had zero direct coverage; a few small `i18n.go` edge branches
  (malformed locale JSON, a missing fallback-locale file, a full BCP-47
  region tag falling back to the base language) rounded the package to
  100%.

### One production change

`cloudsync.go`'s `tickInterval`/`firstDelay` tuning knobs (documented
"overridable in tests") moved from plain `time.Duration` vars to
`atomic.Int64`-backed accessor functions. `Start()`'s spawned goroutine
reads them on every loop iteration; testing `Start()` for the first time
required overriding them to short durations, and a plain var raced the
goroutine's read against the test's write under `-race` (reproduced
consistently, not a fluke). Same default values (2 min / 15 s), same
semantics, zero behavior change for the one real caller
(`internal/app.Run` → `cloudsync.Start`) — confirmed by grep, no other
reader exists.

## Independent review (different model, opus)

**Verdict: SAFE TO MERGE WITH FIXES.** The reviewer re-ran everything
personally rather than trusting the summary: reverted the atomic change to
confirm the race was real (`-race` failed on the very first run, reverted
back), re-ran `-race -count=5` clean on the actual diff, re-confirmed the
`stock_locations` claim by reading the repository query and the test
schema directly, and mutation-tested several of the new assertions
(deleting `apply()`'s `msg == "" → "done"` default, its `remove_plugin`
missing-id guard, `num()`'s string-parse-error check, and hook-error
propagation — all four caught by the intended test).

Two real findings, both fixed pre-commit:

- **F1 — a second instance of the same class of race the atomic fix
  addressed.** `issue_reports_test.go`'s `withTempPendingDir` writes the
  plain `issuereport.PendingDir` var, which a leaked `Start()`-spawned
  goroutine can still be reading (via `Tick → uploadPendingIssueReports →
  issuereport.Pending()`) with no synchronization between them. Reproduced
  by the reviewer at 4/10 runs under `go test -race -shuffle=on` (this
  repo's CI runs plain `go test ./...`, so it wasn't failing today, but a
  real gap regardless).
- **F2 — the two `Start()` tests didn't actually verify goroutine exit.**
  Mutation-proved: deleting either of `Start()`'s two
  `case <-ctx.Done(): return` branches left both `TestStart*` tests passing
  anyway, because they inferred "the goroutine stopped" from "no more
  observed HTTP calls" rather than proving it — a leaked, permanently
  blocked goroutine produced the same observable symptom (no more ticks) as
  a correctly-exited one.

**Fix applied**: replaced the HTTP-call-counting inference with
`waitGoroutineExit` — a helper that polls `runtime.NumGoroutine()` back
down to (at most) a pre-`Start()` baseline before the test proceeds.
Verified this actually closes F2: re-broke each of the two `ctx.Done()`
branches in isolation and confirmed both now fail loudly (goroutine count
never returns to baseline) instead of passing silently, then restored and
confirmed green again.

Getting `waitGoroutineExit` itself correct took two more rounds, both
self-caught before considering the fix final:
1. The shared package-level `httpClient`'s idle keep-alive connections
   (and their read-loop goroutines) persist well past a request
   completing — real, harmless, and unrelated to `Start()`, but
   indistinguishable from a leak by raw goroutine count. Fixed by calling
   `httpClient.CloseIdleConnections()` before each measurement.
2. Two of the three `Start()` tests created their test DB
   (`testDB(t)`, which spawns `database/sql`'s own permanent
   `connectionOpener` background goroutine for the DB's lifetime) **as an
   inline argument to `Start()`, after the baseline was already captured**
   — so that goroutine was never in the baseline, only in the "after"
   count, producing a persistent false "leak" that never resolved within
   the timeout. Root-caused with a temporary diagnostic test dumping full
   goroutine stacks (`runtime.Stack`), which named the exact goroutine
   (`database/sql.(*DB).connectionOpener`) and its origin. Fixed by
   hoisting `db := testDB(t)` before the baseline capture in both tests.

**F1's residual state, disclosed rather than silently left**: even with
`waitGoroutineExit`, `go test -race -shuffle=on` can still occasionally
race `issue_reports_test.go` against a `Start()` test's straggler
goroutine. This is not a timing margin `waitGoroutineExit` could widen —
Go's race detector requires an actual synchronization primitive
(channel/mutex/`WaitGroup`) to establish a happens-before edge; polling
`runtime.NumGoroutine()` after the fact, however accurate in wall-clock
terms, doesn't create one. The real fix is the same one the standing
`app.Run doesn't join its background services on shutdown` queue item
already needs (a done-channel/`WaitGroup` on `Start()`) — building that ad
hoc here would risk conflicting with that item's own in-flight PR #101.
Confirmed clean under this repo's actual CI invocation (plain `go test
./...`, no `-race`) and under `-race` alone at `-count=10`. Logged as a new
sibling instance on that queue item (`ut-docs/QUEUE.md`) rather than fixed
in this batch — matching that item's own established pattern of cataloging
other affected functions (`internal/plugins.Supervisor`, the wasm
runtime's event-channel drainer) as scoped-out follow-ups instead of
silently expanding its diff.

Two small findings, both fixed:

- **F3** — `TestUploadIssueReportSendsMultipartBundle`'s log-field
  assertion (`gotLogFields < 0`) could never fail (`len()` is never
  negative), and the bundle's `Logs` came from the process-wide
  `logging.Recent()` ring buffer — shared, mutable, test-order-dependent —
  so the mutation the doc comment claimed to cover (deleting the entire
  logs-encoding loop, or the `created_at` field) broke no test. Fixed by
  building the `Bundle` directly with known `Meta.Logs`/`CreatedAt` values
  instead of going through `Save()`, and asserting the exact encoded
  values.
- **F4** — `t.Fatalf` called from inside an `httptest` handler goroutine
  (invalid per the `testing` package's own documentation — it `Goexit`s
  the wrong goroutine). Changed to `t.Errorf` + `return`.

Two nitpicks fixed as drive-bys: a dead `_ = okID` assertion in
`TestUploadPendingIssueReportsFullCycle` (now actually verifies the
discarded bundle's directory is gone) and a missing assertion in
`TestInitHonorsEnvOverrides` (`UT_DEFAULT_LOCALE` was set but
`cfg.Locales.Locale` — the field it actually drives, distinct from
`cfg.DefaultLocale`/`UT_MARKETPLACE_LOCALE` — was never checked).

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  both pass (775 i18n keys, all locales matching).
- Full `go test ./...`: clean except one pre-existing, unrelated failure
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, which
  relies on file-permission enforcement this sandbox's root user bypasses
  — confirmed identical on `main` before this batch via `git stash`, not
  caused by this diff).
- `go test ./internal/cloudsync/... ./internal/config/... -race -count=10`:
  clean.
- Coverage numbers independently re-measured, not just quoted: 95.3% /
  100.0%.

## Deliberately out of scope

- `internal/config`/`internal/cloudsync`'s remaining uncovered lines
  (`post()`'s `NewRequestWithContext` error branch, a couple of
  `uploadIssueReport`/`attachFile` multipart-writer error branches,
  `uploadPendingIssueReports`'s `Discard`-failure branch,
  `pushSnapshotIfChanged`'s `it.IsActive` skip — dead code given
  `ListItems`'s own SQL already filters `is_active = 1`): each would need
  invasive mocking disproportionate to the risk they guard against: no
  production bug is plausible on any of them, and they're not reachable
  through any realistic input.
- `Start()`'s missing join/done signal — the root cause behind F1's
  residual state above — logged on the existing
  `app.Run doesn't join its background services on shutdown` queue item,
  not fixed here.
