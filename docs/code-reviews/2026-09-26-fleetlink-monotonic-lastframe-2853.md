# Code review — fleetlink link timing on the monotonic clock (ut-docs#2853)

**Date:** 2026-09-26 · **Lane:** cloud-54 · **Complexity:** easy
**Author:** Sonnet (dev subagent) · **Reviewer:** Opus 5.5, fresh context, detached worktree

## What shipped

`Peer.lastFrame` used to store `time.Now().UnixNano()`. It was read back through
`time.Unix(0, …)`, which has no monotonic reading, so three things ran on the
wall clock: the heartbeat watchdog (`peer.go`), the additional till's
`Status().Linked`/`LostAt` (`client.go` `statusAt`, `markLinkLost`), and the
hub's `PeerInfo.LastFrame`. A forward wall-clock step could drop a healthy link
or show a false outage. Causes include an NTP correction, or a Pi without an
RTC setting its clock after boot. A backward step delayed detection.

- `lastFrame` now holds the nanoseconds since the package-level
  `monoAnchor = time.Now()`. Access goes through three helpers:
  `touch`, `sinceFrame` (monotonic) and `lastFrameAt` (the frame's time placed
  on the caller's wall clock, still carrying a monotonic reading). Because of
  that, `internal/pages/tills_roster.go`'s `now.Sub(p.LastFrame)` stays
  monotonic.
- Tests: `wallStepped` (test-only, `unsafe` over `time.Time`'s layout, and
  guarded by `TestWallStepped`) builds what `time.Now()` returns across a
  wall-clock step. New tests:
  - `TestClient_StatusIgnoresAWallClockStep`: +1h forward stays linked; −1h
    backward, 13 s after the last frame, is still detected as lost.
  - `TestPeer_LastFrameAtTracksAWallClockStep`.
  - `TestHub_PeersSnapshotLastFrame`.

## TDD evidence (re-verified by the reviewer)

With the production files reverted and a shim restoring the old wall-clock
semantics:

```
status_test.go:202: sinceFrame after a +1h wall step = 1h0m0s, want < 12s (the watchdog would wrongly fire)
status_test.go:215: sinceFrame 13s on, after a -1h wall step, = -59m47s, want > 12s
status_test.go:238: lastFrameAt's wall clock moved by 0s since the frame, want ~1h
```

With the fix restored, all of these pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No review record in the WIP diff | Fixed (this file) |
| 2 | minor | `touch`/`sinceFrame` fall back to the wall clock silently if handed a time without a monotonic reading | Fixed: doc note on `touch` saying callers pass `time.Now()` |
| 3 | minor | In the wall-step test, the `sinceFrame` assertion failed first, so the `statusAt` assertion was never exercised on old code | Fixed: `statusAt` is now checked first with `t.Errorf` |
| 4 | minor | `TestHub_PeersSnapshotLastFrame` passes on old code too | Accepted: it is a smoke test of the hub path, and the wall-step cases are covered by the other tests |
| 5 | nit | `link_status.go` `contactAfter` compares `LostSeen` with the wall-clock DB `sync.last_contact_at`, and `LostAt` is fixed at detection. This was already the case before this change | Deferred: ut-docs#2915 |

## Verified

- `gofmt -l`: clean. `go vet`: clean. `golangci-lint`: 0 issues.
- `go build ./...`: clean.
- `go test -race -count=3 ./internal/fleetlink/`: passes.
- `go test ./internal/pages/`: passes. It was run without `-race`, like CI does (#648/#1992).
- CI build-job guards: all pass locally, with two exceptions:
  - `guard-deadcode-baseline`: fails identically on `main` in this container because the GTK headers are missing (`internal/logging` Stderr).
  - `guard-shellcheck-version`: no shellcheck binary in this container.
- No UI, i18n, SQL, file-write or `paths.Data` surface is touched. The change is backend-only and needs no help-topic change.

**Verdict:** safe to merge.
