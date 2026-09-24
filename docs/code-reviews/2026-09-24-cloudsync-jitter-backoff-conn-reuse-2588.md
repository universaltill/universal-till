# Review: cloudsync jittered tick, backoff, connection reuse (ut-docs#2588)

- **Date:** 2026-09-24
- **Branch:** `perf/2588-cloudsync-jitter-conn-reuse`
- **Card:** universaltill/ut-docs#2588 (epic #2587, till↔cloud at 100K tills per region), `complexity:easy`
- **Author:** Sonnet dev subagent; **reviewer:** Opus 5.5, fresh context, isolated worktree

## What shipped

- `internal/cloudsync/schedule.go` (new): the `scheduler` behind `Start()`'s loop.
  - A normal wait is `tickInterval() × U(0.8, 1.2)`, drawn fresh on every tick.
  - The first wait after boot is `firstDelay() × U(0.8, 1.2)`.
  - A failed tick (the heartbeat POST failed) backs off with full jitter: `U(0, min(cap, base·2ⁿ))`, where cap = max(10 min, interval).
  - The backoff has a 5 s floor, which is lowered only when the ceiling is itself smaller. The failure count resets on success.
  - A `Retry-After` on 429/503 is honoured as a minimum, clamped at 1 h. It is ignored on any other status.
- `internal/cloudsync/cloudsync.go`:
  - `httpClient` gets its own transport, cloned from `DefaultTransport` so proxy, timeouts and HTTP/2 carry over. Its `IdleConnTimeout` is 5 min, longer than the 144 s maximum tick, and its idle pool is small.
  - `statusError.RetryAfter` is parsed in `post()`, with its `Error()` text unchanged.
  - Added `parseRetryAfter` and `drainBody`.
  - `Start` draws every wait from the scheduler, via `newSchedulerFn`. `Start` still runs on its own goroutine, so the sale path is untouched.
- `internal/cloudsync/issue_reports.go`: the status-pull non-200 path and the upload path now drain the response body, so the connection goes back to the pool.
- `internal/cloudsync/tick_schedule_test.go` (new) has tests for:
  - jitter bounds and extremes
  - the backoff sequence, cap, overflow and reset
  - Retry-After parsing and the 429/503-only rule
  - `post()` capturing Retry-After
  - connection reuse across two `Tick`s (an httptest server counting `StateNew`)
  - the transport idle timeout being longer than the production maximum tick
  - `Start` actually using the scheduler

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | In production, ingress-nginx's default client keep-alive (75 s) closes the idle connection before the next 96–144 s tick. The client-side 5 min idle timeout alone does not give reuse across ticks. Reuse within a tick does work. | Out of this repo's scope. Filed **ut-docs#2607** (raise ingress keep-alive and set ut-cloud `http.Server.IdleTimeout`). The comment in `newHTTPTransport` now says so. |
| 2 | Low–Med | `parseRetryAfter` overflowed `time.Duration` on huge delta-seconds: `10000000000` gave a negative wait, and `18446744074` gave ~290 ms, which bypassed the 1 h clamp. | **Fixed.** The parser accepts digits only and saturates at `retryAfterClamp` before multiplying. An HTTP-date is capped too. TDD: the test failed with `parseRetryAfter("10000000000") = -2346317h47m53.7s, want 1h0m0s`, then passed. |
| 3 | Low | No test proved that `Start` uses the scheduler. A `Start` reverted to raw `firstDelay()`/`tickInterval()` passed every test. | **Fixed.** Added `newSchedulerFn` and `TestStartDrawsEveryWaitFromScheduler`. Against the bypass mutation it failed with `scheduler drew 0 waits for 2 ticks`; restored, it passes. |
| 4 | Low | The drain on the upload path (`uploadIssueReport`) is not covered by any test. | **Accepted gap.** It uses the same `drainBody` helper whose pull-path use the reuse test does prove: removing it fails with `new connections across 2 ticks = 2, want 1`. A dedicated test needs a pending-report fixture out of proportion to a two-line drain. |
| 5 | Nit | Some comments were inaccurate: the floor "applies to ms tests", "Tick's own error wrapping", and a stray "sibling var block" reference. | **Fixed.** |

## TDD / mutation re-verification (reviewer, isolated worktree)

| Mutation | Result |
|---|---|
| `IdleConnTimeout` back to 90 s | Caught by `TestTransportIdleTimeoutOutlivesProductionTick` |
| Default transport (no `Transport` field) | Caught by the same test |
| Drain removed from the pull non-200 path | Caught by `TestConnectionReuseAcrossTicks` (2 new connections, want 1) |
| Drain removed from `uploadIssueReport` | Survives (finding 4, accepted) |
| `jitteredWait` returns base unchanged | Caught by `TestJitteredWaitBounds` / `TestJitteredWaitExtremes` |
| No failure reset on success | Caught by `TestSchedulerBackoffSequenceAndReset` |
| `retryAfterHint` accepts any status | Caught by `TestSchedulerHonoursRetryAfterOn429And503Only` |
| `Start` bypasses the scheduler | Survived in round 1 → fixed (finding 3), now caught |

## Gate

- `gofmt -l .` produced no output.
- `go build ./...` and `go vet ./internal/cloudsync/` are clean.
- `go test -race -count=1 ./internal/cloudsync/` passes, after the fixes.
- The full `go test ./...` passes.
- `golangci-lint run ./internal/cloudsync/...` reports 0 issues.
- These guards pass: data-access, i18n, kiosk-engine, deadcode-baseline, compliance-claims, competitor-naming, help-topics, help-drift, page-http-error, plugin-menu-read.

This is a backend-only change with no UI, locale strings, help topic, SQL, file writes or paths, so there was no driven UI run. The repo rules on money, i18n, SQL placement, `os.MkdirAll` and `paths.Data` do not apply.

## Verdict

Safe to merge. The one production-effect gap (finding 1) is tracked as ut-docs#2607.
