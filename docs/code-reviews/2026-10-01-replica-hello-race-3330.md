# Review: replica-link tests read the main till's hello before it lands (ut-docs#3330)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54
- **Author:** Sonnet (Dev subagent). **Reviewer:** Opus 5.5 (independent, fresh context, separate worktree)
- **Card:** universaltill/ut-docs#3330 (found by lane:cloud-24 during #3155's full `go test ./...` on a loaded machine)

## What shipped

Test-only. `TestReplicaLink_HelloCarriesItsOwnCloudDeviceID` waited on
`replica.LinkClient.Linked()`. That flag flips when the *main till's* hello
reaches the replica (`internal/fleetlink/client.go`). The test then read
`f.dp.Link.Peer(tillID).Hello()` once. The main till records the
*replica's* hello separately, in `Peer.dispatch` (`internal/fleetlink/peer.go`),
so under load that one read could come back `ok=false`.

A new helper, `waitPeerHello`, polls the hub until the hello is recorded and
then returns it. Both the failing test and
`TestReplicaLink_HelloKicksAPullAndAnAdminChangeArrivesInASecond`, which used
the same single-read pattern, now use it. A hello that is never recorded is
reported as its own failure ("never recorded a hello from the replica"), and
the field assertions are unchanged. Production code is unchanged.

## TDD evidence (reviewer, with injected delays)

The race did not reproduce on its own: original code, CPU hogs, `-race -count=50`.
So it was forced by temporarily editing `peer.go`:

- **Mutant 1:** a 200 ms sleep before `p.hello = &h` for `Role == "replica"`.
  - The original tests fail with the card's exact message:
    - `:120 … (ok=false)`
    - `:186 … (ok=false), want till_id … and cloud_device_id till-cloud-xyz`
  - The fixed tests pass.
- **Mutant 2:** the replica's hello is never recorded. The fixed tests fail in about 3 s each with "never recorded a hello", and nothing hangs. The helper can't hide a real regression.

Gates:
- `gofmt` and `go vet ./internal/pages ./internal/fleetlink`: clean.
- `go build ./...`: ok.
- `go test ./...`: ok.
- `golangci-lint run ./...`: 0 issues.
- Targeted runs: `-race -count=3 -run 'TestReplicaLink_|TestSyncLink|TestSyncPull'` ok, and `-race -count=5 -run TestReplicaLink_` ok after the nit fix.
- CI guards: all pass locally except two, both of which fail the same way on `origin/main` in this container:
  - `guard-deadcode-baseline`: no GTK headers, so the desktop root is skipped and two `internal/logging` funcs look unreachable.
  - `guard-shellcheck-version`: no `shellcheck` binary.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The Dev also added a hello wait in `TestReplicaLink_TableNudgeDoesNotPull`. It isn't needed there: `Hub.Nudge` only needs the peer registered, and that already holds once `Linked()` is true. The test passed under mutant 1 without it. | Fixed: the wait was removed to keep the change minimal. |
| 2 | none | No data race on the helper's captured `h`, because `waitFor` runs `cond` in the test goroutine and `Hello()` copies under `hmu`. | No change. |
| 3 | none | Other hello/peer reads after a client-side wait are already safe: `cloud_checkin_relay_test.go` and `fleetlink/client_test.go` poll, and `fleetlink_test.go:221` reads after an in-order report. | No change. |

## Verdict

Safe to merge. There is no user-visible change, so no help/manual or locale update is needed.
