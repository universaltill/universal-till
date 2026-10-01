# Review: restart watchdog stays quiet while a sale holds the restart (ut-docs#3303)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-24
- **Author:** Opus 5.5. **Reviewer:** Opus 5.5 (independent, fresh context; `complexity:easy` per MODEL-ROUTING.md)
- **Card:** universaltill/ut-docs#3303

## Root cause: a real watchdog race, not only test timing

`restartInto` closed `plan.idleSeen` right after its **first** idle wait,
which comes after the delay. It then stopped the plugins and waited for idle
a **second** time. `restartWatchdog` starts counting `watchdogAfter` (60 s in
production) from `idleSeen`. So a sale that opened while the plugins were
stopping, and stayed open for more than 60 s, raised a false
`selfupdate.restart_pending` Problem ("restart the till to finish the
update") while the restart was correctly held for that sale.

The flaky test hit the same window by timing. Its 10 ms sleep before "a sale
opens" could stretch past the 50 ms delay on a loaded machine. The first idle
wait then passed, and the watchdog (1 ms) fired while the second wait held.

## What shipped

- `internal/selfupdate/pending.go`: on the unattended path (`idle != nil`),
  `idleSeen` is closed after the **last** idle wait, right before the exec.
  The manual path (`idle == nil`) still closes it at once, so its timing is
  unchanged (the #2738 record kept that path byte-identical).
- `internal/selfupdate/pending_test.go`:
  - New `TestWatchdogQuietWhileASaleOpenedWhilePluginsStopHoldsTheRestart`.
    The sale opens inside the hook, which runs after the first idle wait, so
    the interleaving is driven deterministically. It also checks that the
    restart goes ahead once the sale closes.
  - `TestWatchdogQuietWhileASaleOpenedDuringTheDelayHoldsTheRestart` no
    longer sleeps 10 ms before "a sale opens". Its `idle()` now reports idle
    on the first call, whoever makes it, and the sale is open for every
    later call. That is the #2738 scenario with no wall-clock race. It also
    still catches a watchdog that polls `idle()` itself (review finding 1).

## TDD evidence

- Without the `pending.go` change, **both** tests fail. The new test failed
  3/3 runs:
  `watchdog raised {… Key:selfupdate.restart_pending …} while the restart
  was held for a sale`. With the change it passes.
- Card AC: `GOMAXPROCS=1 go test ./internal/selfupdate/ -race -run
  'TestWatchdog|TestRestart' -count=200` passes.

## Findings

No blockers. The reviewer checked that `idleSeen` is closed exactly once on
every path. On the manual path it is closed in the `else` branch. On the
idle path it is closed only before `exec`, so the exec-failure, systemd,
smoke/rollback and signal paths all run after it, as before. The Windows
`handOverToInstaller` path has its own watchdog and is unaffected.
`-race -count=20` passed.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The first version of the de-flaked #2738 test set `busy` before the goroutines started. The till was then never idle, so a watchdog that polls `idle()` itself would pass, and the test's comment ("idle at scheduling time") was no longer true. | Fixed. `idle()` returns idle on its first call and busy afterwards. The test was re-verified: it fails without the `pending.go` change and passes with it. |
| 2 | nit | A reflowed doc comment in `pending.go` ran to about 100 columns. | Fixed (re-wrapped). |
| 3 | note | On the idle path, the watchdog now starts counting up to `hookBound` (15 s) later when the plugin stop is slow. It stays silent for as long as a sale stays open after the plugins stop. | Accepted. This is the intended semantics: a restart held for a sale is not pending (#2738). |

## Gate

`gofmt -l .` (no output), `go build ./...`, `go test ./...` (all packages pass), `golangci-lint run ./...` (0 issues), and the Go-scanning guards `guard-data-access`, `guard-core-neutral`, `guard-kiosk-engine` and `guard-no-showmodal` all pass. After the review fixes, `internal/selfupdate` was re-run, including the 200× `-race` run with `GOMAXPROCS=1`. No user-facing strings, help pages, migrations or money changed.
