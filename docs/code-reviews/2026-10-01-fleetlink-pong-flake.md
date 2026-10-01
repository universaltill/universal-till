# Review — fleetlink: TestLink_PongKeepsPeerAliveAndPingIsAnswered flake (ut-docs#3316)

**Date:** 2026-10-01 · **Lane:** cloud-54 · **Scope:** test-only, `internal/fleetlink/fleetlink_test.go`
**Author:** Sonnet dev subagent · **Reviewer:** Opus 5.5, fresh context (complexity:easy routing)

## What shipped

`TestLink_PongKeepsPeerAliveAndPingIsAnswered` now uses `PeerTimeout = 600ms`
(was 150ms) against `fastConfig`'s 50ms `PingInterval`, with a comment saying
why. The loop still runs for `3 * PeerTimeout` (now 1.8s) and both assertions
(link never drops; our pings get a pong) are unchanged.

## Root cause

The test client writes only when it receives a server ping. The hub's watchdog
(`peer.go` `writeLoop`, `sinceFrame > PeerTimeout` → close 4008) therefore had
only `PeerTimeout − PingInterval` ≈ 100ms of slack. A runner stall of that size
(race detector, loaded CI) let the `check` ticker win before the ping/pong
round trip — the CI failure at ~0.17s. The margin is now ≈ 550ms.

## Verification

- **Reproduced before the fix:** a harness that SIGSTOPs the race-built test
  binary for 130ms every ~400ms failed the old test **16/40** with the exact CI
  message (`4008 "no frame within timeout"`). Plain `-count=300` under CPU load
  at `GOMAXPROCS=1` did not reproduce, so the stall harness is the evidence.
- **After the fix:** same harness **0/40**; `-race -count=20` on the test and
  `-race -count=5` on the whole package pass.
- **The test still bites (reviewer, temporary worktree):** removing
  `p.touch(now)` on inbound frames → fails at 0.61s with 4008; removing the
  server's pong reply → fails with "our pings were never answered with pong".

## Findings

- No blockers, no should-fix.
- Nit (accepted): loop could be `2 * PeerTimeout` to save 0.6s — kept at 3
  windows for a stronger claim.
- Sibling heartbeat tests with tight timeouts (`TestLink_HeartbeatPingsAndPeerGoneAfterSilence`,
  `client_test.go` heartbeat-loss, `status_test.go` lost-link) assert that a
  silent peer *is* dropped; a stall only drops it sooner, so they don't share
  this flake. No action.

**Verdict:** safe to merge.
