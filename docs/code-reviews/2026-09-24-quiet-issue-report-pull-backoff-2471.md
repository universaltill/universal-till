# Code review: quiet the issue-report status pull's repeated-failure logging (ut-docs#2471)

**Change:** `internal/cloudsync/issue_reports.go`, `issue_reports_test.go`. `pullIssueReportStatusesPage` (the till's 2-minute cloud-sync pull of issue-report statuses) logged a `Warnf` on every tick for as long as the cloud endpoint kept returning a non-200 status (e.g. 504 during a single-replica cloud deploy) or a transport error (e.g. DNS failure) — flooding the till's logs and the shop's Problems feed (`logging.Recent()`, ADR-0018, which only remembers Warn+). Scoped to the till side only; the root infra cause (single-replica `unitill-cloud` with no readiness probe, causing real 504 windows during deploys) is split into ut-docs#2512 (`blocked:env` — `homelab-k8s` is a different GitHub org, out of reach for this lane).

**Built by:** Sonnet (session model, inline). **Reviewed by:** Opus (independent subagent, detached worktree).

## The fix

A package-level `pullFailureMu`/`lastPullFailure` tracker plus `logPullFailure(msg)` (Warn if `msg` differs from the last logged failure, Debug if identical) and `clearPullFailure()` (called on a successful page fetch). The two failure sites in `pullIssueReportStatusesPage` — the transport error from `httpClient.Do` and a non-200 response — now go through `logPullFailure` instead of calling `Warnf` directly. Retry behaviour is unchanged (still unbounded, offline-first — only the log level changes).

## Findings and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Some transport errors embed a changing detail (e.g. a local port number), so those won't dedupe across ticks; a page beyond the first also carries a different `offset` in the URL, and a cloud alternating between two status codes (e.g. 502/503) warns every tick | Accepted, not fixed here. The reported symptom — a steady 504, or a repeated DNS failure — is exactly what this fix targets and is covered by the new tests. Comparing by failure *class* (status code / error type) instead of raw message text would generalize this, but that's beyond what #2471 asked for; left as a documented limitation rather than scope creep. |
| 2 | nit | A decode error occurring between two identical status-code failures doesn't reset the tracker, so the second status-code failure logs quietly even though a different symptom happened in between | Accepted — rare (would need a well-formed-but-empty response between two 504s), and the decode-error path itself still warns unconditionally. |
| 3 | optional | During a long outage the Problems feed shows one warning with its original timestamp and nothing after — someone checking days later could read that as "resolved" | Not implemented. A periodic re-warn (e.g. hourly) would need a design call on cadence; out of scope for a logging-noise fix. Left to the product owner if wanted. |

Reviewer also confirmed: concurrency is safe (single mutex around one string, no reentrant logging calls), test isolation is safe (`resetPullFailureTracking(t)` used by the new tests; the one older test that doesn't reset it, `TestPullIssueReportStatusesBestEffortNoops`, asserts nothing about logs so is unaffected), and scope is right — the decode-error path, the request-build error path, and the separate upload-failure logging (which already has its own tracking) were deliberately left untouched.

## TDD verification (reviewer, independently)

Reverted both failure sites back to a direct `Warnf` call: `TestPullIssueReportStatusesRepeatedIdenticalFailureLogsWarnOnceThenQuiet` failed as expected (`WARN count for the repeated 504 = 5, want exactly 1`). Removed only `clearPullFailure()`: the recovery test failed as expected (`WARN count across two separate failure episodes = 1, want 2`). Restored, both pass.

## Gate

`gofmt -l` clean; `go build ./...` ok; `go vet ./...` clean; `golangci-lint run ./...` 0 issues; `go test ./...` ok (full suite, all packages); `guard-data-access.sh` ✓ (no SQL touched); `guard-deadcode-baseline.sh` ✓ (no new unreachable functions). Reviewer additionally ran the changed package with `-race`, `-count=3 -shuffle=on`: all green.

**Verdict:** safe to merge.
