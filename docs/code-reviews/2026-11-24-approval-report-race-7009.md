# Review: approval-key report race in `internal/enroll` (ut-docs#7009)

Pre-flight: till user-visible behaviour n/a (test-only race, runtime unchanged); `web/help/` n/a; `reference/` n/a; `README.md` n/a; no `en.json` keys; no migration, ADR, manifest or host-function change; review record: this file; follow-ups: none.

## What shipped
- `internal/enroll/approval_report.go`: `kickApprovalKeyReport` copies `approvalRetryDelays` under `approvalReport`'s mutex (which it already took) before starting the goroutine, and the goroutine reads only that copy. A report kicked with a nil WaitGroup (`register()`, pair, move) can outlive its test; it no longer reads the package var while a later test swaps it. Nothing outside tests ever reassigns the var, so runtime behaviour is unchanged and the kick stays non-blocking.
- `approval_report_helpers_test.go`: `setApprovalRetryDelays(t, d)` swaps the waits under the same lock and restores them in Cleanup. `TestKickRetryAfterFailureIsSlowAndBounded` uses it instead of a bare assignment.
- New regression test `TestKickedReportDoesNotReadRetryDelaysWhileTheyAreSwapped`: it kicks a report against a failing cloud, then swaps the waits while the report retries (at least 2 attempts), then cancels and joins. It adds no sleeps. The race detector is the assertion, so it only means something under `-race`, which CI runs.

## Review (independent Sonnet 5.5 reviewer; code by Opus 5.5)
Verdict: **approve**.
- Every read and write of `approvalRetryDelays` was checked; only the locked copy and the locked helper remain. The other state the stray goroutine reads (`httpClient`, `Effective()`/`cur`, `approvalkey.Shared()`, `done`/`inflight`, `approvalConflict`) is guarded or never reassigned by tests.
- N1 (nit) — **fixed**: the swap loop registered one Cleanup per iteration. It now writes under the lock directly; the one Cleanup comes from the first helper call.
- N2 (nit) — **accepted**: the test is red only under `-race`. Its comment says so.

## Tester (independent Sonnet 5.5)
- PASS. AC `go test -race -count=20 ./internal/enroll`: `ok` (175 s).
- Red/green: with only `approval_report.go` reverted, the new test fails with `DATA RACE` at `approval_report.go:167` (reviewer: 5 of 5 runs). With the fix, `-race -count=20` passes. Re-checked after N1: red (2 races) and green (`-race -count=10 -run Kick`).
- `gofmt`, `go vet`, `golangci-lint run ./internal/enroll/...` (0 issues) and `guard-fixed-dates.sh` are all clean.

## Verdict
Approve. Merge once CI is green.
