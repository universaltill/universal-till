# Join pages sync/EOD/update loops to app.Run's shutdown drain (ut-docs#153)

## What shipped

`StartSyncPush`, `StartSyncPull`, `StartEODScheduler` and
`StartAutoUpdateScheduler` (`internal/pages/`) ran unjoined on `ctx` and
were not registered on `app.Run`'s shutdown `*sync.WaitGroup`, so they
could still be mid-tick — doing DB work via `internal/data` repositories —
when `database.Close()` ran on shutdown. `StartAutoUpdateScheduler`
additionally calls `selfupdate.Apply`, which renames the binary/web
assets, so an unjoined shutdown mid-swap had a narrow window to leave the
install half-applied.

All four are now wired onto `bgCtx` + `app.Run`'s `wg`, exactly matching
the join shape `StartCloudSync` already uses (`wg.Add(1)` before the
goroutine starts, `defer wg.Done()` inside it). `runSyncLoop`
(`internal/pages/sync_admin.go`), the shared 30s-ticker harness for the
push and pull loops, now takes `wg` so both loops that go through it are
covered by one change. Call sites in `internal/pages/init.go` were updated
to pass `bgCtx` (not `ctx`) and `wg`.

## Scope (rescoped by BA this cycle)

The original ut-docs#153 also named `internal/plugins.Supervisor`'s
`monitorProcess` goroutines and the wasm runtime's per-plugin
event-channel drainer. Verified live against current code that both need
real design work first — dynamically-spawned goroutines with no bounded
join point for the first, a channel nothing closes on shutdown for the
second — not the same mechanical `wg.Add`/`Done` pattern as the four loops
here. Split out to **universaltill/ut-docs#380** rather than folded into
this diff; `complexity:hard` → `complexity:medium` on #153 to match the
narrowed, now genuinely mechanical scope.

## Tests

Four new regression tests (`TestStart*_JoinsWaitGroupAndExitsOnCtxCancel`,
one per function, in each function's own `_test.go`): start the loop with
a cancellable `context.Context` and a real `*sync.WaitGroup`, then confirm
the goroutine exits (`wg.Done()`) within 2s of cancelling.

Confirmed test-first (signature change from `Start(ctx, d)` to
`Start(ctx, d, wg)` made the pre-existing call sites fail to compile until
the fix landed).

`TestRun_JoinsBackgroundGoroutinesOnEarlyServerError`
(`internal/app/app_test.go`) already existing — drives a real boot through
`pages.Init` (so all four loops start) into an early `server.Start` bind
failure with the caller's own `ctx` never cancelled — continues to pass in
well under the 10s drain timeout, proving `bgCtx` (not the untouched
caller `ctx`) is what actually stops these loops on this path.

## Independent review

Opus subagent (complexity:medium routing). **Verdict: PASS, no
blocker-class findings.**

- Confirmed the join shape is correct at all 5 sites (`wg.Add`
  synchronous before `go func()`, `wg.Done` deferred and unconditional,
  each goroutine selects on the passed-in `ctx` parameter, not a captured
  outer context).
- Confirmed all four `init.go` call sites pass `bgCtx`, not `ctx` — and
  **proved this is test-protected**: reverting one call site back to
  `ctx` in a scratch copy made
  `TestRun_JoinsBackgroundGoroutinesOnEarlyServerError` fail exactly as
  designed (`Run took 10.93s (>= the 10s drain timeout)`).
- Confirmed no double-add/deadlock risk (each `Start*` has exactly one
  non-test caller).
- Confirmed zero diff on `internal/plugins/supervisor.go` and
  `internal/plugins/wasm_runtime.go` — the #380 split is clean.
- Confirmed no SQL added, no money/i18n/offline-first surface touched,
  and the doc-comment rewrites in `app.go`/`init.go` accurately describe
  the new state.
- **Real finding (non-blocker, fixed same round):** the four new tests as
  first written didn't actually prove the join — `wg.Wait()` on a zero
  counter returns immediately, so removing `wg.Add`/`wg.Done` entirely
  from the source was invisible to them (reviewer demonstrated this by
  gutting the wiring and watching all four still pass). Fixed by adding a
  pre-cancel assertion that `wg.Wait()` is still blocking (the counter is
  genuinely non-zero) before cancelling — independently re-verified this
  round: gutting `StartEODScheduler`'s `wg.Add`/`defer wg.Done()` now
  fails `TestStartEODScheduler_JoinsWaitGroupAndExitsOnCtxCancel` with an
  explicit message, and restoring the fix passes again.
- Ran the full targeted gate itself (`go build`, `go vet`,
  `go test ./internal/pages/... ./internal/app/... ./internal/cloudsync/... -race`,
  `guard-data-access.sh`) — all green. Noted
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` fails in
  this environment (root ignores the `0o500` dir permission it relies on)
  — pre-existing and unrelated; the diff touches zero files under
  `internal/issuereport`.
- Nitpicks not actioned: `TestStartSyncPull`'s no-op `refresh` closure
  means `syncPullTick` itself is never exercised by that test (now noted
  in a comment, since that fix was cheap); the `init.go` call-site
  ordering (StartSyncPush ~37 lines above the other three) is cosmetic.

## Verification (this round, post-fix)

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/... ./internal/app/... ./internal/cloudsync/... -race -count=1` —
  all green.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
  clean.
- `go test ./... -race` — green except the pre-existing, unrelated
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
  (confirmed it fails identically on unmodified `main`, untouched by this
  diff).
- Manually gutted and restored `StartEODScheduler`'s wg wiring to confirm
  its regression test actually catches the class of bug it's named for.
- No UI/visible surface touched (internal shutdown-lifecycle plumbing
  only) — no manual/help-topic or screenshot update needed.

## Safe to merge

Yes. Feature branch `fix/153-shutdown-drain-pages-loops`, merged via
`merge` (not squash/rebase, per this pipeline's standing merge-method
rule).
