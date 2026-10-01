# Review: pin the redial draw in TestNextAttemptDuringBackoff

- **Date:** 2026-10-01
- **Lane:** lane:cloud-54 (follow-up to universaltill/universal-till#1568, ut-docs#3312)
- **Branch:** `fix/cloudlink-next-attempt-flake`
- **Author:** Opus 5.5. **Reviewer:** Fable (independent subagent, fresh context)

## What shipped

`main` CI at `d27cb38` went red. `internal/cloudlink`
`TestNextAttemptDuringBackoff` failed with `timed out waiting for: a
next-attempt time after an abnormal close`.

After an abnormal close, the client waits `spread() + backoff(1)` before
redialling: with `fastOptions` that is 20 ms × r1 + 5 ms × r2, using the
real rng. `wait` stores `nextAttempt` and clears it when the timer fires.
On a draw near 0 the window is far shorter than `eventually`'s 2 ms poll,
so the poll never sees it. `233854a` fixed the identical flake in
`TestNextAttemptClearsWhenTheTimerFires`. This change applies the same fix
here: a 200 ms spread and `rng` pinned to the top of it, set through
`newHarnessClient` before `Run` starts.

Test-only, one file (`internal/cloudlink/client_test.go`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The comment should say the delay is spread + backoff, not the spread alone | **Fixed** |

The reviewer checked every other test that polls `NextAttempt`:
- `TestRetryAfterReasonThenFailedDialReconnects` has a 1 s window, so it is safe.
- Its backoff poll self-heals, because 200 dropped dials are queued.
- `TestNextAttemptClearsWhenTheTimerFires` is already pinned.

None has the same exposure.

## Verified

- Reproduced: the old setup with `rng` forced to 0 fails with the exact CI
  message, 3 runs out of 3.
- Fixed: `go test -race -count=30 -run TestNextAttemptDuringBackoff` passes,
  and `go test -race ./internal/cloudlink` passes repeatedly (author: 3 runs,
  reviewer: 5).
- `go vet` and `gofmt` are clean. The test costs about 0.2 s more per run.

## Verdict

Safe to merge.
