# Review: goroutine-recover guard extended to mobile/ (ut-docs#3313)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-24
- **Author model:** Sonnet (Dev subagent, `complexity:easy`). **Reviewer model:** Opus 5.5 (independent, fresh context).
- **Branch:** `fix/3313-mobile-goroutine-recover`

## What shipped

1. **Guard walks `mobile/` too.** `TestNoUnrecoveredGoroutines` now calls
   `findUnrecoveredGoroutines(root, "internal", "cmd", "mobile")`. The planted
   test adds a bare `go` in `mobile/m.go` and expects it to be reported.
2. **The Android `app.Run` goroutine recovers.** In `mobile/mobile.go`
   `runOnce`, the goroutine now starts with
   `defer logging.RecoverAndLog("mobile.run")`, then
   `defer close(newInst.done)`. Deferred calls run last-in first-out, so on a
   panic `done` is closed first and the panic is then logged (redacted) and
   recovered. Before this, a panic in `app.Run` killed the whole Android app
   with an unredacted stack on stderr, and `done` never closed.
3. **Test seam `runApp`** (`var runApp = app.Run`, unexported, so gobind
   ignores it). `TestStart_PanicInRunIsRecoveredAndClosesDone` swaps in a
   func that panics. It asserts that `Start` fails fast with "server exited
   before becoming ready" and that `IsRunning()` is false.
4. **`plugins/` is not walked (AC 3).** Its only bare goroutine is the
   `plugins/tax-tr/okc/sim` TCP simulator, and `plugins/tax-tr` is moving out
   of core (ut-docs#2878). The reason is in the guard's doc comment.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | `done` closes before `RecoverAndLog` writes the log line, and `err` stays nil on a panic. So `Start` reports only "server exited before becoming ready", and the panic line may land in till.log a moment after the waiter resumes. | Accepted. A single recovering closure that also sets `err` would break the guard's required `defer logging.RecoverAndLog` first-statement shape. The panic is still logged. |
| 2 | nit | The new test does not assert that the panic was logged. | Accepted. `RecoverAndLog`'s logging has its own tests in `internal/logging`. This test covers what is new here: the process survives and `done` closes. |
| 3 | nit | Only the top-level `app.Run` goroutine is in scope. | No change. Goroutines started inside `app`/`server` are already covered by the `internal/` walk. |

The reviewer also checked these:
- **Race on `err` and `done`.** Every reader receives from `done` before it reads `err`.
- **Test isolation.** `runApp` cannot race with another test, because no `mobile` test uses `t.Parallel` and `mobileTestEnv` uses `t.Setenv`.
- **gobind.** No exported signature changed.
- **Other `go` statements.** `mobile/` has no other non-test `go` statement.

## Verified

- **Red → green, re-run by the orchestrator.**
  - With only the `mobile/` walk added, `TestNoUnrecoveredGoroutines` failed and reported `mobile/mobile.go:327`.
  - With the old goroutine body restored, `TestStart_PanicInRunIsRecoveredAndClosesDone` crashed the test binary (`panic: boom from app.Run`).
  - With the fix, it passes (`-count=3`).
- **Race detector, run by the reviewer.** `go test -race ./mobile/ ./internal/logging/` passes.
- **Gate, all clean:**
  - `gofmt -l .` printed nothing.
  - `go build ./...` passed.
  - `golangci-lint run ./...` reported 0 issues.
  - `go test ./...` passed everywhere except one failure in `internal/fleetlink` (`TestLink_PongKeepsPeerAliveAndPingIsAnswered`). That package is untouched by this diff, and the test passed 5/5 on re-run.
  - Every `bash scripts/ci/*.sh` guard in the `build` job passed, apart from the two environment-only failures below.
- **Environment-only gate failures, not this diff:**
  - `shellcheck` is not installed in this container, so the `shellcheck` and `guard-shellcheck-version` steps could not run. CI runs them, and no `.sh` file changed.
  - `guard-deadcode-baseline` flags `logging.Stderr` and `timestampWriter.Write`. Both are used only from `cmd/unitill-desktop`, which the guard skips without GTK/WebKit headers. It fails the same way on `main`.
- **No UI change.** Nothing a shop operator sees changes, so no help topic needed updating.

## Verdict

Safe to merge.
