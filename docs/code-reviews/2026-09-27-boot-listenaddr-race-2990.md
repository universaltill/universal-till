# Code review: boot data race — server.Start wrote the shared config (#2990)

- **Card:** universaltill/ut-docs#2990 (bug, p3, complexity:medium)
- **Author:** Opus 5.5 (lane:cloud-54, inline). **Reviewer:** independent
  Fable subagent in an isolated worktree.

## What was wrong

`go test ./internal/app/ -race -run 'TestRun_AcquiresLockAndStillBootsNormally|TestRun_AdoptsPrinterCharsetDefaultOnBoot'`
reported a data race on every boot. It reproduced on `main` (d9b2918).
`server.Start` wrote the bound address back into the shared
`*config.Config` (`cfg.ListenAddr = actualAddr`). By then `pages.Init` had
already started the cloud-link goroutine, which calls
`enroll.Effective(cfg)` and so copies the whole struct (`out := *cfg`).
The race is real in production too, not only in tests. CI's `go test`
runs without `-race`, so CI never saw it.

## What shipped

- `internal/server/server.go`: `Start` keeps the bound address in its own
  `actualAddr`. The log line, the browser-open and
  `plugins.SetTillListenAddr` all use it. `Start` now only reads `cfg`.
- `internal/server/server_test.go`:
  - `TestStart_ServesOpensBrowserAndShutsDown` used to assert the old
    write-back. It now asserts that the browser opens the bound
    `127.0.0.1:<port>` and that `cfg.ListenAddr` is unchanged.
  - New `TestStart_DoesNotWriteSharedConfigWhileOthersReadIt` copies
    `*cfg` in a loop while `Start` binds. The race detector flags any write
    under `-race`. Its final assertion fails without `-race` as well, so
    plain CI `go test` also guards the fix.
- `internal/app/recovery_mode_test.go`: removed a comment that described
  the old write-back.

## Why the fix is sufficient

The reviewer checked the whole boot path:
- Before this fix, `server.Start` held the only write to `*cfg` after
  `pagesInit` started goroutines.
- The earlier writes (`LoadRuntimeConfig`/`SaveRuntimeConfig`,
  app.go ~209) run before any `go` statement captures `cfg`, so a
  happens-before edge orders them.
- No non-test code writes through `Deps.Cfg`.
- Nothing reads `cfg.ListenAddr` after `Start` expecting the bound
  address. Recovery mode binds `cfg.ListenAddr` only when boot fails,
  in which case `Start` never ran.

So after boot the shared config is effectively immutable, and it needs no
mutex.

## TDD evidence (reviewer re-verified in its worktree)

With `server.go` reverted to `origin/main`, both `TestStart_*` tests fail:
- `Start rewrote cfg.ListenAddr to "127.0.0.1:…"`
- `WARNING: DATA RACE` at server.go:273

With the fix restored, all `TestStart_*` tests pass. The two boot tests
named on the card pass under `-race`. So do the whole `internal/server`
and `internal/app` packages.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor, pre-existing | The mDNS advertiser (app.go:374) gets the *configured* port. If the configured port is busy and `Start` binds another one, the advertised port is wrong. | Accepted, no card. A fallback bind of a wildcard host is loopback-only (#1169), so the advertised port is only ever wrong for a till the LAN cannot reach. `listenPort`'s doc also says nothing consumes the port yet (#185). |
| 2 | nit | A comment in `server.go` read as contradictory, and `listenAddr := actualAddr` was a pure alias. | Fixed: the alias is removed and the comment reworded. |
| 3 | nit | The `recovery_mode_test.go` comment referred to the removed write-back. | Fixed. |
| 4 | nit | The new test sleeps 200ms. | Accepted. It is not flaky in the failure direction: the final assertion runs after `Start` returns, and the race detector does not depend on timing. |

## Verified beyond automated tests

This is backend-only: no UI, i18n, SQL, migrations or help topics are
touched. The full gate ran: gofmt, build, vet, `go test ./...`, `-race`
on server and app, golangci-lint, and every `build`-job guard.

**Verdict:** safe to merge.
