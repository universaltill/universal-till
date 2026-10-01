# 2026-10-01 — cloudlink: the test fake cloud pings like the real one (ut-docs#3295)

`TestLinkVersionIsRecordedBeforeTheKick` failed on universal-till#1548's CI
run (`timed out waiting for: kick on nudge`). Just before that, the log showed
`cloudlink: link dropped (timed out: true); redialling`.

**Root cause (a test bug, not runner noise).** The test's `fakeCloud`
(`internal/cloudlink/helpers_test.go`) sent `hello` and then nothing else
unless the test sent a frame. The test harness sets the till's fleetlink
`PeerTimeout` to 2 s. On a loaded runner, the test sometimes took that long to
send the nudge. The till then dropped the silent link as dead and redialled,
and the nudge went out on the dead socket and was lost. The real cloud pings
every link (`fleetlink.Peer.writeLoop`), so a real link never goes silent
like that.

**Fix, test-only. Product code is unchanged.**
- `cloudConn.pingUntilClosed`: the fake cloud pings every 50 ms (the harness
  `PingInterval`) until the connection closes. Its reader drops the till's
  `pong` replies along with `ping`s, so keepalives can't fill the 64-slot
  `frames` buffer.
- New regression test `TestQuietCloudLinkOutlivesPeerTimeout`. It sets
  `PeerTimeout` to 300 ms and stays quiet for 3× that. The nudge must still
  kick, and the till must have dialled exactly once.

**Verification.**
- TDD: the new test fails against the old `helpers_test.go` with the same
  symptom as CI (`kick on nudge`, after `link dropped (timed out: true)`). It
  passes with the fix. Checked by the author and again by the reviewer.
- The card's AC: `TestLinkVersionIsRecordedBeforeTheKick` and the new test
  passed 200/200 with `GOMAXPROCS=1` and `-race`, with 4 CPU-hog loops on 4
  cores. After the review nits, both passed 100/100 with `GOMAXPROCS=1 -race`.
- `go test -race ./internal/cloudlink` passes. `go build ./...` and
  `go test ./...` pass.

**Review (Fable, independent): safe to merge, no blockers.**
- The reviewer checked that concurrent `Write`s are safe
  (coder/websocket v1.8.15: every method except Read/Reader is safe to call
  concurrently).
- No goroutine leaks: `c.closed` is closed when the handler returns, and
  cleanup stops the client before the server.
- A `null` ping payload is fine: `peer.go` touches liveness before decoding,
  and its ping handling doesn't read the payload.
- No existing test relies on the cloud staying silent.
- Nit, fixed: the till's pongs could pile up in `frames`. They are now dropped.
- Nit, fixed: the new test's margin. `PeerTimeout` went from 150 ms to 300 ms.

Deferred: none.
