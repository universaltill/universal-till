# Code review — mobile.Start: notice a moved bind, retry on a fresh port (ut-docs#3290)

- **Date:** 2026-09-30
- **Branch:** `fix/3290-mobile-bind-moved`
- **Author:** lane:cloud-24 (Opus 5.5). **Reviewer:** independent Fable subagent in its own worktree.
- **Complexity:** medium.

## What shipped

`main` went red on `TestStart_PersistedPortBusy_FallsBackAndPersistsTheNewOne`
(30.09s, "server did not become ready within 30s"). The cause:
`mobile.start` probes a port free, closes the probe and lets `app.Run` bind it
later. If something takes the port in that gap, `server.listenWithFallback`
binds `127.0.0.1:<port+1>` (the #1169 loopback degrade), but `waitUntilReady`
kept polling `127.0.0.1:<port>` until it timed out. A real phone has the same
gap: another app can take the port.

- `internal/server`: `WithBoundAddr(ctx, func(actual string, moved bool))`.
  `server.Start` calls it right after the bind and before serving, using the
  existing `movedOffConfiguredAddr` rule. The value reaches `server.Start`
  through `app.Run`'s `bgCtx`, which derives from the caller's ctx, so no
  signature changed. Callers that don't set it see no change.
- `mobile`: `start` runs each boot through `runOnce`. When a bind moves,
  `waitUntilReady` returns `*bindMovedError` at once. `start` then tears the
  instance down and retries on a fresh `freePort()`, bound on `0.0.0.0`
  again, for up to `maxBindAttempts` (3) tries. After that it returns an
  error that says it gave up. Only the port really served is persisted.
- `waitUntilReady` counts a 200 as ready only after our own server has
  reported a non-moved bind. Recovery mode (ADR-0075) never reports a bind,
  so its 503 plus `recovery.HeaderMode` answer is still ready by itself.
- `scripts/ci/deadcode-baseline.txt`: `WithBoundAddr` is called only from
  `mobile/`, which is not one of the guard's roots. Same class as the existing
  `SetDeviceModel`, `SetAndroidBridge` and `procrestart.SetRestarter` entries.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A moved bind could still count as ready if whatever held the port answered `/healthz` 200. The poll accepted any 200 before the server reported its bind, then persisted the stolen port and handed the WebView a stranger's address. The reviewer reproduced it with a throwaway test (3.4ms, no retry). The gap predates the branch, but the branch's comment claimed otherwise. | **Fixed.** A 200 counts only after our server reports a non-moved bind. New tests: `TestWaitUntilReady_200WithoutABindReportIsNotReady` and the end-to-end `TestStart_PortTakenByAnHTTPServer_RetriesInsteadOfTrustingIt`. Both fail without the guard and pass with it. |
| 2 | nit | The `instance.bound` doc comment was misleading. | Fixed. |
| 3 | nit | When retries ran out, the error didn't say that three ports were tried. | Fixed: it now wraps with "gave up after 3 ports…", and `errors.As` still works. |
| 4 | nit | `waitUntilReady` rebuilt the wanted address with a `portOf` helper. | Fixed: `instance.listenAddr` holds it, and `portOf` is removed. |
| 5 | note | A `Stop()` during a retrying `Start` does nothing, because `inst` is still nil. | Accepted. Same behaviour as before (`inst` was always set only once ready). The window is now up to three boots long. |

The reviewer also checked these and found no problem:
- Races (`-race` clean). `bound` is a size-1 buffered channel with a non-blocking send.
- Teardown between attempts: cancel, then `<-done`, the same path as `restartInProcess`.
- `UT_LISTEN_ADDR` is re-set before each `app.Run` reads config.
- The retry loop always ends.
- Recovery and demo modes are unchanged.
- `listenport.Save` creates its directory. No cwd-relative paths.
- No new exported gomobile surface.

## Verified beyond the automated tests

- **Red→green, done by the author and again by the reviewer.** With the moved-bind check disabled, `TestStart_PortTakenBeforeBind_RetriesOnAFreshPortAndPersistsIt` fails with main's exact error (`server did not become ready within 30s`, 30.09s). With the fix it passes in 0.73s.
- The same red→green check for finding 1's two tests.
- Gate:
  - `gofmt`, `go build ./...` and `golangci-lint` (0 issues) are clean.
  - `go test -race ./...` passes.
  - `go test -count=5 ./mobile/` passed in the reviewer's run, before the finding-1 fix. After the fix, `go test -race ./mobile/ ./internal/server/` and `go test -count=3 ./mobile/` pass.
  - Every `ci.yml` guard passes locally except two that are local-only:
    - `guard-shellcheck-version`: no shellcheck binary here.
    - `guard-deadcode-baseline`: two `internal/logging` entries whose callers are in the GTK-only desktop root, which this container can't analyse. CI does.
- No UI change, so there is no UX pass and no help-topic change.

## Verdict

Safe to merge.
