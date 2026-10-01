# Review: goroutine-recover guard extended to cmd/ (ut-docs#3305)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-24
- **Author model:** Sonnet (Dev subagent, `complexity:easy`). **Reviewer model:** Opus 5.5 (independent, fresh context, isolated worktree).
- **Branch:** `fix/3305-goroutine-recover-cmd`

## What shipped

1. **Guard walks `internal/` and `cmd/`.** `TestNoUnrecoveredGoroutines` now
   resolves the repo root and walks both subtrees through
   `findUnrecoveredGoroutines(root, "internal", "cmd")`. Violations are
   reported repo-relative (`cmd/unitill-desktop/control.go:158`). The bare
   `RecoverAndLog` exemption is keyed to the exact `internal/logging`
   directory, so a `cmd/…/logging` package is not exempt. The planted test
   adds a bare `go` under `cmd/`, a `cmd/x/logging` impostor (both reported)
   and a file outside the walked subtrees (not reported).
2. **All 5 bare `go` statements in `cmd/unitill-desktop`** now start with
   `defer logging.RecoverAndLog(…)`: the self-re-exec watcher, the
   `stopChildServer` reaper, the shell-mode poll watcher, the control
   server's `Serve` and the child `cmd.Wait` reaper. Recovering is always
   better than crashing here, because a dead shell closes the till window.
3. **The stdout guard stays `internal/`-only.** This is a recorded decision
   (AC 3), and the doc comment says why. `cmd/` entry points own their
   process's standard streams: `unitill-uninstall` is a terminal CLI, and the
   desktop shell wires the child server's streams. Widening the guard would
   need an exception on nearly every hit.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `desktop.go`: the comment claimed a reaper panic falls back to "the timeout path". In fact `waitForChild` then sees only ready or timeout, so an early child exit is reported as a timeout. | Fixed (comment reworded). |
| 2 | minor | `mobile/mobile.go:327` (Android `app.Run` goroutine) is still unwrapped and outside the guard's walk. | Out of scope. Filed as ut-docs#3313. |
| 3 | nit | Doc comment on `findUnrecoveredGoroutines` had a ~150-character line and hardcoded the subtrees. | Fixed. |
| 4 | nit | The `webview_fallback.go` "LIFO" comment misattributed why `close(pollDone)` runs on panic. | Fixed (reworded). |

The reviewer also checked:
- **Defer order and hangs.** `close(pollDone)` still runs during unwinding, so the shutdown join cannot hang. `stopChildServer`'s selects all have `time.After` bounds, so a reaper panic costs at most ~15 s. A control-server panic leaves the child on `NoopWindowController`, the same as a failed bind.
- **Paths and walk.** Windows path separators are handled (all `filepath`). A missing subtree fails loudly through the walk error. Build tags are ignored by the parser, so the desktop, linux and windows files are all scanned.

## Verified

- **Red → green, re-run by the reviewer.** With `cmd/` reverted, `TestNoUnrecoveredGoroutines` fails listing exactly the 5 sites. With `cmd/` restored, it passes.
- **Gate.** All of these passed: `gofmt -l .` (empty), `go build ./...`, `go vet ./...`, `go test ./... -count=1`, `golangci-lint run ./...` (0 issues).
- **Desktop shell, built locally against GTK/WebKit headers.** All passed: `go build -tags desktop ./cmd/unitill-desktop`, `go vet -tags desktop`, `go test -tags desktop ./cmd/unitill-desktop`.
- **Build-job guards.** All passed: data-access, kiosk-engine, no-showmodal, i18n, compliance-claims, competitor-naming, help-topics, help-drift, core-neutral, pipefail-grep-q, deadcode-baseline, readme-local-links, page-http-error.
- **No runtime UI change.** Nothing a shop operator sees changes, so there is no help-topic update.

## Verdict

Safe to merge.
