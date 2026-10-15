# Code review — main-till chip turns unreachable after a clean shutdown (ut-docs#4422)

**Date:** 2026-10-15 · **Lane:** `lane:cloud-41` · **Author model:** Opus 5.5 ·
**Reviewer:** independent Sonnet 5.5 subagent (Fable 5.1, the mapped reviewer
for a medium card, was refused: out of usage credits — MODEL-ROUTING
fallback).

## What shipped

On an additional till, the status bar's main-till chip read **polling** for
~61 s after the main till was shut down cleanly (SIGTERM → its hub sends
`bye` on every link), versus 2 s after a crash. `fleetlink.Client` treated
every bye as "not an outage" (`clearOutage`), so `deriveLinkView` rule 4
never saw a loss and the chip waited for PrimaryWatch's 90 s window.

- `internal/fleetlink/client.go`: a link that ends with a bye now records
  `byeAt` (`markBye`). `statusAt` reports `LostAt = byeAt`,
  `LostSeen = byeAt + PeerTimeout` once one presence window (12 s) has
  passed with no link and nothing answering. `markLinkUp`, `markAnswered`
  (also on a 401 probe now) and `clearOutage` clear it — a main till that
  restarts and answers within the window is still no outage.
- `deriveLinkView` is unchanged; its rule-4 comment says a bye counts once
  the window passes. A pull that reaches the main till after `LostSeen`
  still restores **polling**.
- No new WARN or heartbeat problem: `OnLost` is still only called for a
  dropped link, never for a bye.
- Help: `web/help/{en,de,ar,tr}/multitill.md` say the amber light also
  appears when the main till was closed or restarted and hasn't come back
  within those seconds (`fa` has no such sentence). `make docs-shots`:
  screenshots byte-identical, manifest refreshed.

## Tests (TDD)

- `internal/fleetlink/client_bye_test.go`: bye then gone → lost after one
  window, since the bye; bye then back within the window → no outage; bye
  then a probe answer without a link → no outage; bye then a probe answer
  with the dial refused (503) → no outage.
- `internal/pages/link_status_test.go`
  `TestLinkChip_CleanShutdownTurnsWithinOnePresenceWindow`: the real main
  till handler/hub (`newSyncLinkFixture`), a real link client and the real
  chip endpoint; `dp.Link.Close()` (the same call `StartSyncLink` makes on
  shutdown) then the server goes. The chip turns **unreachable** within
  about one presence window, with a time.
- Watched both fail on `main` first (chip stayed "polling", LostAt never
  set). The reviewer reverted `client.go` independently and saw the same
  failures, then passes once it was restored.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | "back within the window" test can't catch a byeAt lingering after a relink (statusAt ignores it while linked) | accepted: the guarded behaviour is the relink itself |
| 2 | nit | `markAnswered` clearing (probe OK, dial refused) had no test | fixed: 503 test added |
| 3 | nit | a 401 probe after a bye left byeAt, so a restarted-then-refusing main till read unreachable | fixed: `markAnswered()` in the `ErrUnauthorized` branch |
| 4 | nit | pages test: a retry probe between the two closes could relink once and push past the 1 s bound | fixed: 2 s slack (also on the fleetlink gone test) |
| 5 | — | doc comments accurate; locking (`smu` copy), monotonic clock (`byeAt` from `time.Now()`, compared via `heardAfter`) correct; no lingering byeAt path found | — |

Reviewer flake runs: `-race -count=15` (fleetlink bye tests), `-race
-count=10` (pages clean-shutdown test), all pass. One unexplained failure of
`-count=3` while the reviewer's race runs shared the 4 CPUs; not reproduced
in 50+ later runs, including under load. The bounds were widened anyway.

## Verified beyond automated tests

- The production shutdown path (`StartSyncLink` → `d.Link.Close()` → bye)
  is the one the pages test drives.
- **Not done:** a driven two-process run (main on :8092, additional till on
  :8093, real SIGTERM). The #3605 Tester's run is the reproduction. The
  integration test covers the same code over real sockets, but not process
  exit timing.
- No visual surface changed: the chip's states and words are as before, and
  only the moment it turns changed.

## Gate

`gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues), `go test
-race ./internal/fleetlink/`, `go test ./internal/pages/ ./internal/pages/common/`
(CI's race job leaves `internal/pages` out too), and `go test -race ./...`.
That was green apart from `internal/db`, `internal/cloudsync`, `internal/pages`
and `internal/pages/common`, which hit this 4-core container's 10/20-min
`-race` timeouts (`db` and `cloudsync` don't depend on `fleetlink`). Also
every guard in `ci.yml`'s `build` job
(`guard-shellcheck-version.sh` needs shellcheck, not installed here; CI
has it).

**Verdict:** safe to merge.
