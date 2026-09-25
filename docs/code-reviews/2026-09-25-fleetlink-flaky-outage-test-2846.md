# Review: flaky fleetlink outage test (ut-docs#2846)

**Branch:** `fix/2846-fleetlink-flaky-outage-test` · **Author lane:** lane:local (Opus 5.5) · **Reviewer:** independent Fable subagent

## What shipped
Test-only change to `TestClient_StatusRecordsWhenAnEstablishedLinkWasLost`,
which failed release run 36188196961 (v0.24.1) and flaked 10/200 locally
under `-race`.
- The fake "silent" main till accepted redials and sent a fresh hello, so
  the client relinked (real contact: `markLinkUp` clears the outage) and a
  second outage started ~PeerTimeout later. `refuse` is now set as soon as
  the first hello arrives, so only one link is ever established.
- Reviewer finding (MEDIUM, 1/300 after the first fix): the test read
  `Status()` twice; the second read could land in `runLink`'s defer between
  `setLinked(false)` and `markLinkLost` and see `LostAt` zero. The test now
  keeps the one snapshot that `waitFor` observed. Fixed.

No product code changed: the client's behaviour (a hello ends an outage)
is right.

## Verification
- Before: 10/200 failures (`-count=200 -race`).
- After both fixes: 0/500 (`-count=500 -race`, 148 s); full
  `./internal/fleetlink` package `-race` green; `go vet` clean.
- Reviewer checked the other silent/refuse tests in the package
  (`HeartbeatLossCountsAFailedContact`, `HonoursRetryAfterOn503`, …): none
  assert on `LostAt` timing, no shared race.
