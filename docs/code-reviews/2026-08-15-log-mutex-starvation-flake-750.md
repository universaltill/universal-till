# Code review — third-recurrence leaked-goroutine flake in internal/plugins (ut-docs#750)

**Date:** 2026-08-15
**Branch:** `pipeline/750-log-mutex-starvation-flake` → `main`
**Repo:** universaltill/universal-till
**Author (Dev):** Claude (Fable), autonomous SDLC pipeline
**Reviewer:** Claude (Opus), independent subagent, isolated worktree
**Complexity:** hard

## What shipped

Test-only fix in two files, `internal/plugins/publish_reload_race_test.go` and
`internal/plugins/manager_close_sequencing_test.go`. Root cause: a background
publisher goroutine started before a loop that can call `t.Fatalf`. `t.Fatalf`
unwinds via `runtime.Goexit()`, which runs any registered `defer`/`t.Cleanup` but
skips plain trailing statements — so a drain (`close(stop); wg.Wait()`) written
*after* the loop never ran on a Fatal exit, leaking the goroutine. The leaked
goroutine kept calling `bus.Publish` → `auditEventWithDB` against the test DB,
which gets closed at cleanup, producing an infinite `sql: database is closed` log
storm that starves the shared `logging.Logger` mutex for whatever test runs next in
the same `go test` process — occasionally hanging the whole package for the full
10-minute default timeout. This is the third recurrence of a root cause already
fixed twice in `internal/plugins/shutdown_drain_test.go` (ut-docs#509,
universal-till#294); this fix mirrors that file's `t.Cleanup`-registered-before-
the-risky-code pattern rather than inventing a new one.

- **`publish_reload_race_test.go`** (`TestPublish_NeverPanicsRacingManagerReload`,
  the ticket's named target): drain moved into a `t.Cleanup` registered
  immediately after the goroutine starts, before the 100-iteration reload loop.
  Redundant trailing manual drain removed (would double-close `stop` otherwise).
- **`manager_close_sequencing_test.go`**
  (`TestManagerClose_NeverPanicsWhenSequencedAfterPublisherDrain`, found via this
  ticket's own AC #4 sweep instruction): trickier — the goroutine and its drain
  live *inside* a 300-iteration loop, and the manual `close(stop); wg.Wait()`
  immediately before `m.Close(...)` is not incidental, it's the exact
  drain-before-Close ordering the test asserts. Fix: an idempotent `drain` closure
  (`sync.Once` around `close(stop)`, then `wg.Wait()`), registered as a
  `t.Cleanup` safety net right after the goroutine starts *and* still called
  manually at the original site, preserving the ordering under test.

## Independent verification of the mechanism (not taken on the Dev report's word)

The reviewer worked in an isolated git worktree (`ut-docs#386`'s standing rule —
never revert-then-restore on the orchestrator's shared checkout) and, for **both**
changed files, reverted to the `origin/main` shape, injected a forced
`t.Fatalf` on an early loop iteration, and measured the storm by counting
`database is closed` lines with a following test in the same process
(`TestPublish_BlockingHandlerDoesNotWedgeBus`) so starvation had a victim to hit:

| File | pre-fix storm lines | post-fix (same forced fatal) |
|---|---|---|
| `publish_reload_race_test.go` | 53,155 | 0 |
| `manager_close_sequencing_test.go` | 30,155 | 0 |

Both reproductions showed exactly the ticket's described mechanism, beginning the
instant the next test in the process started. Post-fix, the same forced fatal
fails cleanly at the injected `t.Fatalf` with zero storm lines — the publisher is
joined before the process moves on.

## Correctness analysis

- **`sync.Once` drain is correct.** `close(stop)` happens exactly once;
  `wg.Wait()` is documented-safe to call repeatedly. The cleanup and the manual
  call can never run concurrently — `t.Cleanup` funcs run on the test goroutine
  strictly after the body returns/Goexits, so the two calls are sequential, not
  racing.
- **No loop-variable capture bug introduced.** `go.mod` targets Go 1.25 (per-
  iteration loop variables); `stop`/`publisherWg`/`stopOnce` are all declared
  inside the loop body, so each `drain` closes over its own iteration's state.
  Confirmed empirically across 15× `-race` runs with no data race reported.
- **`t.Cleanup` LIFO ordering is correct — the load-bearing question for this
  fix.** `managerTestDB(t)` registers the DB's own `t.Cleanup(db.Close)` first,
  before either test's publisher-drain cleanup is registered. LIFO means the
  drain cleanup fires *before* the DB closes, in both files — the required
  order, and the reason the zero-storm result holds.
- **Mechanism note that also validates sweep scope:** `runtime.Goexit` still
  runs deferred/`t.Cleanup`-registered calls — it only skips plain trailing
  statements. So only non-deferred trailing teardown is vulnerable to this class;
  anything already using `defer`/`t.Cleanup` for its drain was never at risk.

## Sweep completeness (ticket AC #4)

Reviewer independently enumerated every goroutine site in
`internal/plugins/*_test.go` (`go func()` and bare `go fn()` forms, plus
non-`go func` background starters like `StartPlugin`/`httptest.NewServer`).
Beyond the two fixed sites, every remaining goroutine is either bounded (a
single call into a buffered channel) or self-terminating on its own listener/
context close, and none combines *unbounded loop + non-deferred stop + closed-DB
access* — the specific shape this bug needs. The closest look-alike,
`supervisor.StartPlugin`'s `monitorProcess`, only runs with restart policies
`Enabled: false` in this package's tests, so a leak there is bounded to one
closed-DB write, not a storm; the unbounded-restart-loop variant of that same
risk lives in `shutdown_drain_test.go` and was already fixed by ut-docs#509.

**`wasm_sync_test.go` and `wasm_tcp_test.go`: confirmed no fix needed**, matching
Dev's claim — `wasm_tcp_test.go`'s `startTCPFixture` registers its `t.Cleanup`
before its accept goroutine starts and self-terminates on listener close (no DB
access, different shape). `wasm_sync_test.go`'s two tests are both safe, though
for two different reasons — one via the same `t.Cleanup(wg.Done)` pattern as the
fix, the other via a `defer bus.ResetSubscribers()` that survives Goexit and
closes the drainers' channels directly. (Dev's own report gave the first reason
for both; the reviewer confirmed the conclusion holds but corrected the reasoning
for the second test.)

## Gates run (independently, in the isolated worktree)

`go build ./...` clean · `go vet ./...` clean ·
`go test ./internal/plugins/... -race -run 'TestPublish_NeverPanicsRacingManagerReload|TestManagerClose_NeverPanicsWhenSequencedAfterPublisherDrain' -count=15 -v`
— 30/30 pass, no races (111.9s) ·
`go test ./internal/plugins/... -race -count=1` — full package clean (495.1s) ·
`go test ./...` — 37 packages, zero failures · `bash scripts/ci/guard-data-access.sh`
✓ · `bash scripts/ci/guard-kiosk-engine.sh` ✓ · `bash scripts/ci/guard-plugin-menu-read.sh` ✓.

CLAUDE.md compliance: test-only change, no SQL/i18n/money/offline-first/kiosk/
plugin-signing surface touched — each genuinely assessed, not skipped. No
user-visible behaviour, so no help-topic/README/ADR obligation.

## Non-blocking notes (no action taken here)

- **Package sits close to its own timeout ceiling.** `internal/plugins -race`
  runs in ~495s against `go test`'s 600s default — only ~1.7 minutes of
  headroom. Not caused by this diff (test-only; the new cleanups are no-ops on
  the success path) but directly relevant to a ticket about this exact package
  hanging on its 10-minute timeout: an unrelated slowdown could reproduce a
  timeout failure for a different reason. Filed as a follow-up Backlog card
  (ut-docs#753) rather than expanded here.
- `t.Cleanup(drain)` is registered every one of 300 loop iterations in
  `manager_close_sequencing_test.go`, so a normal run accumulates 300 no-op
  cleanup closures at test end. Bounded and cheap; a comment now says this is
  intentional so a future reader doesn't "fix" it into something more
  complicated.
- `shutdown_drain_test.go` (the precedent file itself) and `supervisor_test.go`
  both still have a couple of non-deferred teardowns after a `t.Fatalf` that can
  leak one bounded goroutine each — not this flake's storm class (no unbounded
  loop against a closed DB), out of scope for this ticket, noted for awareness
  only.

## Safe-to-merge verdict

**Yes.** The fix is correct, minimal, and genuinely mirrors the established
ut-docs#509 precedent rather than a superficial copy. TDD claims were
independently reproduced (leak confirmed pre-fix, clean post-fix) on both changed
files, not just the one the ticket named. The sweep required by the ticket's own
AC #4 was verified complete for this bug's specific shape. No blockers found.
