# Approval-key report goroutines no longer outlive a test (ut-docs#7153)

Pre-flight: till user-visible behaviour n/a (no UI, copy, help, migration or
API change; production behaviour unchanged, the stop context is cancelled only
by a test helper).

Built by Sonnet (Dev). Tested by a fresh Sonnet Tester. Reviewed by Opus 5.5
(different model from the author). Orchestrated by routine `:41`
(`lane:cloud-41`).

## Problem

`TestRecover_HealsAndAdoptsTheTokenOnce` failed now and then under `-shuffle`
with a `t.TempDir` cleanup error, "secrets: directory not empty".
`kickApprovalKeyReport(ctx, m, nil)` (Recover's heal, pair, move, claim) starts
a goroutine that nothing tracks, on `context.WithoutCancel`. It calls
`approvalkey.Shared().Report`, then `LoadOrCreate`, which can write into the
test's data dir while it is being removed. A report that had failed parked on a
15-minute timer and could be woken inside a later test.

## Change

- `internal/enroll/approval_report.go`: a package `approvalBG` tracker
  (WaitGroup plus a stop context). Every report goroutine registers with it.
  Stop is checked before each attempt, ends an in-flight request
  (`context.AfterFunc`), and ends the retry wait. Production never cancels it.
- `internal/enroll/approval_report_helpers_test.go`: `resetApprovalReport`
  cancels the stop context and waits for every report goroutine before it
  clears state, then installs a fresh one. `resetState` calls it, and it is the
  cleanup that `recoverTill` registers last, so it runs before the TempDir
  removal.
- `internal/enroll/approval_report_7153_test.go`: the regression test. It kicks
  a nil-WaitGroup report against a server that answers 500 (the report parks on
  its timer), resets, and checks the live goroutine stacks.

## Verification

- **RED on old code.** Reverting both production and helper changes makes the
  regression test fail: "the kicked report goroutine is still running after
  resetApprovalReport". Both the Tester and the Reviewer reproduced this
  independently.
- **Card AC.** `go test -shuffle=on -count=20 -run TestRecover
  ./internal/enroll/` passes. `-race -count=50 -run 7153` passes.
  `-race -count=300` of the card's test passes.
- **Mutations** (Tester): dropping the `AfterFunc` cancel, the reset's
  `wg.Wait`, the reset's cancel, or `wg.Done` is caught. Dropping the
  pre-attempt `stop.Err()` check is not caught. That check only stops a
  disk touch before the first attempt, so it is accepted as a small gap.
- **Gate.** `gofmt -l .`, `go build ./...`, `go vet` and
  `golangci-lint run ./...` (0 issues) are clean. `go test ./...` fails only
  in two places, and both fail the same way on clean `main`:
  - `internal/enroll` `TestCanRecover/no_key`. That is ut-docs#7164, a separate
    card and a different race: Init's own tracked reporter creates a key after
    the subtest switches dirs. It failed 19 of 300 runs on the old code.
  - `internal/plugins` times out locally and does not import `enroll`.

## Review findings (Opus 5.5, verdict: approve)

No High or Medium findings.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| L1 | Low | WaitGroup reuse window if a kick ever overlaps a reset (test-only, cannot happen today) | Documented on `approvalBG` |
| L2 | Low | The comment said a racing kick is "stopped and waited for". It may in fact be stopped before its first attempt. | Comment corrected |
| L3 | Low | The 1 MiB `runtime.Stack` buffer could truncate silently and miss a live frame | The buffer now grows until the stacks fit |
| L4 | Low | A reset called from inside a report goroutine would wait on itself (nothing does this) | Documented on `approvalBG` |
