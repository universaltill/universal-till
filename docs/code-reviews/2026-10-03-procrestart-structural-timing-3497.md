# Review — procrestart tests: structural timing instead of a 5 ms wall-clock race (ut-docs#3497)

**Date:** 2026-10-03 · **Lane:** cloud-24 · **Complexity:** easy
**Author model:** Opus 5.5 · **Reviewer model:** Fable (independent subagent, fresh worktree)

## What shipped

- `internal/procrestart/procrestart.go`: one test seam, `sleep = time.Sleep`,
  used by `scheduleRestart` for the flush delay. No behaviour change.
- `internal/procrestart/procrestart_test.go`:
  - `holdSleep` stubs the flush delay with a gate the test releases;
    `restartReturns` requires `Restart()` to return (2 s bound) while that
    gate is held.
  - `TestRestartSchedulesDelayedReexecOfOwnExecutable` and
    `TestRestartRunsBeforeRestartHookBeforeReexec` no longer assert
    `time.Since(start) < reexecDelay` (5 ms); they assert ordering.
  - `TestRestartHookStartsConcurrentlyNotAfterSleep` no longer measures a
    200 ms wall-clock gap; the hook must start while the delay is held.
  - `TestRestartRunsBeforeRestartHookBeforeReexec` now also holds the hook
    open past the end of the delay, so it proves the re-exec waits for the
    hook (review finding 1).

## Original failure reproduced

Old test binary under CPU load (12× nproc busy loops, `GOMAXPROCS=1`,
`-race`, 1000 iterations of the two affected tests): **192 failures**,
`Restart() blocked for ~50ms — it must schedule, not wait`. New tests under
the same load: 200 full-package iterations, **0 failures**.

## Mutation checks (each regression the tests guard, re-verified by hand)

| Mutation in `scheduleRestart` | Caught by |
|---|---|
| run synchronously (no goroutine) | all three rewritten tests (`restartReturns` times out) |
| hook started after the sleep | `TestRestartHookStartsConcurrentlyNotAfterSleep` |
| restart called before the sleep | `TestRestartSchedulesDelayedReexecOfOwnExecutable` |
| `<-hookDone` removed | `TestRestartRunsBeforeRestartHookBeforeReexec` (new after review) |

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `TestRestartRunsBeforeRestartHookBeforeReexec` still passed with `<-hookDone` deleted (the hook always finished before the delay ended). | **Fixed** — hook gated open past the delay; mutation now fails. |
| 2 | minor | File header's seam list omitted `sleep`. | **Fixed.** |
| 3 | nit | After a failed test, releasing the held sleep in Cleanup could let the parked goroutine reach the restored real `reexec` (exec the test binary). | **Fixed** — `stubSeams` leaves `reexecFn` a no-op when `t.Failed()`. |
| 4 | nit | Remaining short absence waits (20–50 ms) in other tests. | **Accepted** — absence checks can only false-pass, never flake red; out of scope. |

Reviewer: no races (`-race -count=50`), no goroutine leaks in passing runs;
the `sleep` seam follows the existing `reexecDelay`/`reexecFn` pattern.

## Gate

`gofmt -l` clean, `go vet`, `go build ./...`, `golangci-lint` 0 issues,
`go test -race -count=30 ./internal/procrestart` pass. CI guards run
locally: all pass except two environmental ones — `guard-shellcheck-version`
(no shellcheck in the container) and `guard-deadcode-baseline` (skips
`cmd/unitill-desktop` without GTK headers, so `internal/logging` funcs used
only there read as dead; this diff touches neither). CI runs both for real.

**Verdict:** safe to merge. No user-visible change; no help/doc update needed.
