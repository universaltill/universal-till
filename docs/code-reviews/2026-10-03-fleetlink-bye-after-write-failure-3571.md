# Review — fleetlink: a write failure no longer pre-empts a queued bye (ut-docs#3571)

**Date:** 2026-10-03 · **Lane:** cloud-24 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

- `internal/fleetlink/peer.go`: after a non-timeout write failure (the
  `CloseNormal "write failed"` branch of `Peer.write`), `writeLoop`'s
  deferred close first waits for the reader to finish, bounded by
  `min(WriteTimeout, 1s)` (`drainReader`), and only once our own hello went
  out. `run()` closes the new `readDone` channel when `readLoop` returns.
  A main till's `bye` that already arrived is now read, so a planned restart
  ends as `endBye` instead of `endLost` + `OnLost`. A real drop is still lost
  (ADR-0114 §4): its reader errors at once, or the window passes.
- `internal/fleetlink/client_write_fail_test.go` (new file, so it does not
  collide with universal-till#1667's edits to `client_test.go`):
  deterministic fake-conn tests — queued bye after a write failure →
  `endBye`, no `OnLost`; no bye → `endLost`, exactly one `OnLost`, conn
  closed within ~WriteTimeout.

## Verification

- TDD: the bye test fails on `origin/main`'s `peer.go` 20/20
  (`link ended as 0, want endBye (5)`) and passes 20/20 with the fix
  (`-race`).
- The card's load repro (`go test -c -race`, `-test.cpu 1 -test.count=200
  -run 'TestClient_ReconnectsAfterMainTillRestartWithoutCountingAFailure$'`,
  6 `yes` burners on 4 cores, 6 batches): **3/6 batches fail on main**
  (`a bye counted as a failed contact`), **0/6 with the fix** (run twice,
  before and after the review fixes).
- `gofmt -l .` clean, `go build ./...`, `go vet`, full `go test ./...` green,
  `golangci-lint run` 0 issues, `ci.yml` build-job guards pass except two
  container-only gaps unrelated to this Go-only diff
  (`guard-deadcode-baseline.sh` skips `cmd/unitill-desktop` without GTK
  headers and so flags `internal/logging` funcs only that root uses;
  `guard-shellcheck-version.sh` has no shellcheck binary here).
- No UI surface touched → no UX gate, no help-topic change.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The bound test allowed 1 s, not the configured ~WriteTimeout; a hard-coded 1 s wait would pass | **Fixed**: asserts `2 × fastConfig().WriteTimeout` |
| 2 | minor | Draining also ran when our own hello write failed, widening a window where a peer hello flaps the link up/down | **Fixed**: drain only after `helloSent` |
| 3 | minor | Hub/Session close can wait ≤1 s longer if a write fails mid-flush during shutdown (parallel across peers; ms in practice since a dead socket's reader errors at once) | Accepted, recorded here |
| 4 | nit | A 10 ms sleep before sending the bye added flake exposure, nothing deterministic | **Fixed**: removed (test still fails 20/20 on main) |
| 5 | nit | `min(WriteTimeout, 1s)` now appears twice (drainReader, writeBye) | Accepted — two short call sites, kept local |

Reviewer also confirmed: no races (`-race -count=3`), `readLoop`/`writeLoop`
only run via `run()` (no deadlock path), and coder/websocket leaves the fd
open after a non-timeout flush error, so the fake conn models the real
transport.

## Verdict

Safe to merge. No deferred items.
