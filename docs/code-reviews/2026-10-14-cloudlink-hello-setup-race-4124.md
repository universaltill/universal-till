# Review — cloudlink tests: cloud hello state set before the till dials (ut-docs#4124)

**Date:** 2026-10-14 · **Lane:** `lane:cloud-41` · **Author:** Dev subagent at the
`complexity:easy` builder tier · **Reviewer:** independent subagents on a
different model from the author, fresh context (round 1 + a scoped
re-review of the fix round)

## What shipped (test-only)

- `internal/cloudlink/helpers_test.go` — `newHarnessAll(t, cloudMod, mod,
  cmod)` is the one harness constructor; `cloudMod` runs on the `fakeCloud`
  **before** `go Client.Run`. `newHarness` / `newHarnessClient` delegate to
  it; new `newHarnessCloud` wrapper. `waitLimit` (15 s) is the ceiling for
  positive waits (`eventually`, `nextConn`, `frame`) and `harness.stop`;
  they return as soon as the condition holds, so passing runs are no slower.
  Negative windows (`noFrame`, the 300 ms sale-rate window) are unchanged.
- `client_test.go`, `relay_test.go` — the four tests that set fake-cloud
  hello state after construction (`linkVersion` ×3, `liveView` ×1) now set
  it through `newHarnessCloud`/`newHarnessAll`.
- `TestLinkVersionIsRecordedBeforeTheKick` — its `Kick` stub no longer
  reads `h` (assigned only after the constructor returns, which a now-prompt
  kick can beat — `-race` flagged it); it captures the `*Client` in `cmod`
  and counts locally. The "LinkVersion is 0 before any hello" check moved
  into `cmod`, i.e. before Run, the only point where it is still true.

## Root cause

The first build of this card (Dev) treated the CI failure as a slow
runner and only raised the 3 s ceilings. Round-1 review found the real
cause: `newHarness` starts `Run` immediately, and the tests stored
`linkVersion = 5` afterwards. The fake cloud reads `linkVersion` when it
accepts the socket, so if the till dialled first the hello carried
`link_version: 0`, which equals `lastVersion` → no kick, and the link then
stays up on pings with no second hello: the kick never comes, at any
timeout. That is exactly `timed out waiting for: kick on hello`. Production
`client.go` is unchanged: its hello→kick path loses no signal (a link that
drops before Run reads the hello redials and the next hello still kicks,
since `lastVersion` only moves where it kicks).

## TDD

- Repro (Dev and round-1 reviewer, independently): on the original test
  code, a 20 ms `time.Sleep` before `h.cloud.linkVersion.Store(5)` (standing
  in for the test goroutine being descheduled) fails 3/3 with
  `timed out waiting for: kick on hello` — even with the 15 s ceiling
  (15.02 s each); round 1 saw 8/8 fail with 2 ms.
- With the fix, the same 20 ms sleep right after the constructor passes
  3/3, also under `-race`.
- `go test ./internal/cloudlink -race -cpu 1 -count=5` passes.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | Raising the timeout masks the real cause: hello state stored after `Run` dials (3 `linkVersion` tests + 1 `liveView` test). | Fixed — `newHarnessCloud`/`newHarnessAll`. |
| 2 | minor | `harness.stop` still panicked at 3 s, the ceiling this card calls too short; the panic runs in `t.Cleanup`. | Fixed — `waitLimit`; panic kept (a hung Run must not pass silently). |
| 3 | minor | If the first link drops under load, `TestLinkVersionIsRecordedBeforeTheKick` sends its nudge on the dead first socket and "kick on nudge" can't arrive. | Accepted — needs both a 200 ms write or 2 s peer timeout to trip; far less likely than #1; not seen in CI. |
| 4 | nit (production) | In `runLink`, a hello can lose the select race to `done`; the next `attempt` drains it. Not lost (next link's hello kicks); only delayed until a check-in if the close code routes to `waitCheckIn`. | Accepted — delay, not loss; out of this test-only card's scope. |
| 5 | major (fix round) | Deterministic hello exposed a data race: `Kick` stub read `h` before the constructor returned. | Fixed — `cl` captured in `cmod` (before `go Run`). |

**Scoped re-review of the fix round:** safe to merge, no blocker/major.
Confirmed every fake-cloud field a hello reads (`linkVersion`, `liveView`)
is now written only in a `cloudMod` before `go Run`; the one
`queueRefusal` after Run (`TestRevokedStopsUntilTheCredentialChanges`) is
intentional and isn't hello state. `cl` is set in `cmod` before the `go`
statement (happens-before). Control run on the base commit with the same
20 ms sleep after `newHarness(...)` fails with the CI message; the branch
passes 3/3 under `-race`. Minor/nits accepted: the moved
`LinkVersion()==0` check now only pins a new Client's zero value (the
`atKick == [5 9]` assertion carries the test); three thin constructor
wrappers keep existing call sites unchanged.

## Verified beyond automated tests

No UI, runtime or manual surface: test-only change in one package.
Gate: `gofmt -l`, `go build ./...`, `go vet`, `golangci-lint run`,
`guard-data-access.sh` clean; full `go test ./...` green except
`internal/plugins`, which hit the default 10 m per-package timeout locally
(untouched package; CI runs it with `-timeout 20m`).

## Verdict

Safe to merge.
