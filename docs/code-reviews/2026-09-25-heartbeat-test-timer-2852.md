# Review: heartbeat silence test timer (ut-docs#2852)

**Branch:** `fix/2852-heartbeat-test-timer` · **Author lane:** lane:local (Opus 5.5) · **Reviewer:** independent Fable subagent

## What shipped
Test-only: `TestLink_HeartbeatPingsAndPeerGoneAfterSilence` failed main CI
(run 36192050425) with "dropped after 149.2ms, want ≈ 150ms". The hub's
silence clock starts in `newPeer` at accept, which is before `h.dial`
returns; the test took `start` after the dial. `start` is now taken before
dialling, so `lastFrame >= start` follows from the upgrade request/response
ordering. With the watchdog's strict `>` check and tickers that never fire
early, `el >= PeerTimeout` is guaranteed.

## Findings
- Reviewer confirmed the diagnosis (`hub.go:164-188`, `peer.go:108,416,431`,
  `helpers_test.go:167`); upper bound (PeerTimeout+1s) still meaningful.
- Out of scope, filed as a Backlog card: `lastFrame` is wall-clock
  `UnixNano`, so the watchdog (and `client.go:177,501`) runs on the wall
  clock; an NTP step could trip it early. Product-side, not this change.

## Verification
- `-run TestLink_HeartbeatPingsAndPeerGoneAfterSilence -race -count=500`: 0 failures
  (reviewer: 300/300); full package `-race` 3/3 green; `go vet` clean.
