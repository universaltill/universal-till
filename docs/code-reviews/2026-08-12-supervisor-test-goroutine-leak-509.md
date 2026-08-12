# Code review — Supervisor test goroutine leak (ut-docs#509)

- **Date**: 2026-08-12
- **Card**: universaltill/ut-docs#509, `complexity:hard`
- **Change**: `internal/plugins/shutdown_drain_test.go` — test-only fix, no
  production code changed.
- **Author (this cycle)**: scrum-master pipeline, inline (Sonnet). The
  root cause had already been precisely diagnosed by a dedicated
  investigation subagent (file:line, exact fix shape) before any code was
  written, so the actual change is two small, mechanically-specified
  additions in one file — judged not to warrant a Fable dev subagent
  (routing exists to protect quality on genuinely open-ended work; this
  was fully pre-scoped). Independent review still ran at the card's
  mapped `hard`-tier model (Opus), per the `reviewer` skill's routing —
  deliberately not skipped just because the build was cheap.
- **Reviewer**: independent subagent, Opus, fresh context (no dev
  reasoning carried over), on the actual branch (read-only — the fix
  needed no revert/restore verification since it's pre-existing tests
  gaining a missing drain, not a bug fix with a new regression test).

## What was broken

CI intermittently hung ~8-11 minutes on `TestHandleEvent_AskResultRedactedInRealLog`
(`internal/plugins/wasm_runtime_ask_log_integration_test.go`) — a test
completely unrelated to the actual cause. Root cause, confirmed by
investigation before any fix was written: two *other* tests in the same
package (`internal/plugins/shutdown_drain_test.go`) start a real
crash-looping `Supervisor` and only called `supervisor.Shutdown(ctx)` as
a plain statement *after* a polling loop that could `t.Fatal` first.
Since `t.Fatal` unwinds via `runtime.Goexit()`, a fatal in the poll loop
meant `Shutdown` was never reached — leaking the still-restarting
`monitorProcess` goroutine past the test, which then kept retrying
against the test's own (closed via `defer db.Close()`) database, spamming
`logging.Errorf("data.plugin.audit_raw: sql: database is closed")` fast
enough (10ms backoff in the worse of the two) to starve the shared
`log.Logger` mutex and hang whatever test ran next in the same `go test`
process for as long as the leaked loop kept running.

This had recurred at least four times across 2026-08-09 and 2026-08-12
(logged on the issue), twice against `main` itself, not just PRs.

## Origin — confirmed, not guessed

`TestSupervisor_Shutdown_AfterCrashRestartCycleDrainsCleanly` (from
ut-docs#380's original PR #268) and
`TestSupervisor_Shutdown_DoesNotBlockOnMonitorProcessRestartBackoff`
(from ut-docs#502's PR #269, which copy-pasted the same antipattern into
a new test rather than introducing it). ut-docs#503/#504 — floated in the
card as possible candidates — construct no `Supervisor` anywhere in their
diffs and are unrelated, confirmed by reading both PRs' full diffs.

## The fix

In both leaking tests:
- `defer db.Close()` → `t.Cleanup(func() { db.Close() })`, registered
  **first** (right after `setupTestDB`).
- A new `t.Cleanup(func() { _ = supervisor.Shutdown(ctx) })`, registered
  **second**, right after the crash-loop `StartPlugin` call succeeds.

`t.Cleanup` runs strict LIFO (last registered, first called) on every
exit path — normal return, `t.Fatal`, and panic — so the Shutdown drain
now always runs, and always runs *before* the DB closes, on every path,
not just the success path the old code implicitly assumed. Calling
`Shutdown` twice (once via cleanup, once via the test's own pre-existing
explicit call on the success path) is safe — the second call finds an
already-empty process map and an already-drained `WaitGroup`.

## What the independent review found

**Verdict: ACCEPT AS-IS.** The reviewer treated the LIFO-ordering claim
as the load-bearing property of the whole fix and verified it three ways
before accepting anything else:
- Read the Go 1.25.0 toolchain's actual `testing.go` source
  (`Cleanup`/`runCleanup`, the `tRunner` defer, `doPanic`) to confirm
  strict LIFO on all three exit paths.
- Wrote and ran a standalone throwaway test to empirically confirm the
  ordering, and — importantly — confirmed that plain `defer`s run
  *before* all `t.Cleanup`s. That means the `defer db.Close()` →
  `t.Cleanup` conversion was **required** for the fix to work, not
  cosmetic: keeping the `defer` and only adding the Shutdown cleanup
  would have closed the DB *first*, reproducing the exact race under
  review.
- Re-derived `Supervisor.Shutdown`'s and `monitorProcess`'s actual
  control flow line-by-line to confirm the fix addresses the root cause
  (the monitor goroutine is genuinely joined, cancellation is checked at
  both re-entry points under `s.mu`) rather than just muting the symptom,
  and that any audit writes still possible during a live Shutdown are
  bounded by `MaxRestarts` and happen while the DB is still open — zero
  closed-DB writes reachable post-fix.
- Independently re-ran `go test ./internal/plugins/ -race -run
  'TestSupervisor' -count=10 -timeout 2m -v` (clean, 10/10) and `go vet
  ./internal/plugins/...` (clean) itself, not trusting the implementer's
  earlier run.

Four non-blocking observations, none requiring a change to this diff:
1. The `t.Cleanup` drain in these two tests intentionally uses
   `context.Background()` (unbounded), not a deadline — correct, since a
   bounded ctx would let Shutdown return early and reintroduce the
   close-under-live-monitor race; a genuinely undrainable monitor now
   hangs until `go test -timeout` instead of failing fast, an acceptable
   tradeoff.
2. A third test in the same file
   (`TestSupervisor_Shutdown_WaitsForMonitorProcessGoroutines`) still
   uses `defer db.Close()` and has its own `t.Fatal` paths — verified
   benign (its policy is `RestartPolicy{Enabled:false}`, no crash loop,
   so the monitor sees `stopped` and returns without any audit write on
   any exit path) — a reasonable, deliberate scope boundary, not a gap in
   this fix.
3. **Unrelated to this fix, but worth recording for CI**: a full `go test
   ./internal/plugins/ -race -count=1` genuinely takes ~310s on the
   hardware available to this session — the wazero/WASM fixture builds
   dominate, not the supervisor tests (confirmed both under `-race` and
   in isolation). ut-docs#509's own acceptance criterion
   (`-race -count=5 -timeout 5m`) is unreachable on this session's
   hardware purely on wall-clock grounds, independent of any bug — ~11s
   from flaking on a single `-count=1` run alone. GitHub Actions runners
   are presumably faster, and CI has not shown this specific timeout
   symptom (only the storm-caused hangs this fix resolves), so not
   treated as a blocker — flagging in case CI capacity ever tightens.
4. Style/idiom: the added `t.Cleanup(func() { db.Close() })` without
   `_ =` matches the dominant repo-wide pattern (~20 sites); the `_ =
   supervisor.Shutdown(ctx)` next to it is consistent with this file's
   own existing style, not a new inconsistency.

## What was verified beyond the review

- `go build ./...` clean.
- `go test ./... -count=1` — full repo suite, all packages green
  (1m24s).
- `go test ./internal/plugins/ -race -run 'TestSupervisor' -count=10
  -timeout 2m -v` — 10/10 clean, both fixed tests and every sibling
  Supervisor test, run independently by both the implementer and the
  reviewer.
- `go test ./internal/plugins/... -race -count=1 -timeout 12m` — full
  package, single run, passes clean in 313.8s, zero `sql: database is
  closed` storm (one incidental, unrelated, single-occurrence error from
  a different test — not the reported symptom).
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all green (this diff touches no SQL, no
  self-order routes, no plugin-menu reads; run per the repo's standing
  pre-commit rule regardless).
- No real client/shop name in this diff (test-only, uses `com.test.*`
  placeholder plugin IDs already established in the file). No
  secret-shaped literal introduced.
- Manual/help-topic check: test-only change, no shop-owner-visible
  behavior — the `web/help/` rule does not apply.

## Deferred / explicitly out of scope

- `TestSupervisor_Shutdown_WaitsForMonitorProcessGoroutines`'s own
  `defer db.Close()` — verified benign for its specific policy, left
  as a possible minor follow-up, not filed as a new card (no live
  symptom to justify a card for it).
- Any change to CI's own `-race` timeout budget for this package — noted
  above as a real, pre-existing wall-clock risk independent of this bug,
  but not actioned here since it's an infra/CI-tuning decision, not part
  of fixing #509, and no observed CI failure attributes to it (yet).

## Verdict

**Safe to merge.** The root cause is fixed, not just quieted — verified
by tracing the actual control flow, not just re-running tests and
checking green. 10/10 clean `-race` runs of the previously-leaking tests,
a clean full-package `-race` run with no storm, and a clean full
non-race repo suite.
