# Code review — production shutdown doesn't wait for async print/kitchen/invoice goroutines (ut-docs#513)

- **Date**: 2026-08-12
- **Card**: universaltill/ut-docs#513, `complexity:medium`
- **Change**: `internal/pages/init.go`, `internal/app/app.go`,
  `internal/app/app_test.go`, `internal/pages/print_api_test.go`,
  `internal/pages/common/deps.go` (comment only).
- **Builder**: Sonnet subagent, fresh context, design (repo placement,
  the `pagesInit`-seam-vs-full-`Run()`-integration-test decision, the
  `asyncWorkDrainTimeout` sizing) handed to it pre-scoped by the
  orchestrator after investigating the actual call graph
  (`pages.Init`'s only production/test callers, `app.Run`'s defer/LIFO
  ordering, `Deps.AsyncWork`'s existing test-level coverage).
- **Reviewer**: independent subagent, Opus, fresh context, on the real
  branch (no worktree isolation needed — review-only, no revert/restore
  verification required since this is a new feature-wiring fix, not a
  pre-existing-test-gains-a-missing-drain case like #509).

## What was broken

`common.Deps.AsyncWork` (a `sync.WaitGroup` added by ut-docs#425) tracks
best-effort goroutines fired *after* an HTTP handler already responded —
`printReceiptAsync`/`printKitchenAsync` (`internal/pages/print_api.go`)
and the invoice-issue thermal-print goroutine (`internal/pages/invoice_page.go`).
It was wired into test cleanup (`internal/pages/self_order_shop_test.go`)
but **nothing in production shutdown called `WaitForAsyncWork`**.
`internal/app/app.go`'s `Run` has its own, separate `wg *sync.WaitGroup`
for background *services*, joined via `drainBackgroundServices` inside a
deferred cleanup that runs before the earlier-registered
`defer database.Close()` — but the `*common.Deps` instance carrying
`AsyncWork` was constructed entirely inside `pages.Init` and never
surfaced back to `Run`, so there was no way to join it. At real process
shutdown, a print/kitchen-ticket/invoice-print goroutine could still be
reading `printer.*` settings or writing an audit row through `d.Db` while
`database.Close()` ran underneath it — the production instance of the
exact class ut-docs#380 already fixed once for `WasmRuntime`'s drainer
goroutines, reopened via this second, uncoordinated join point.

## The fix

- `pages.Init` now returns `(http.Handler, *common.Deps)` instead of just
  `http.Handler` — its only production caller (`app.go`) and its one test
  caller (`internal/pages/init_test.go`, unchanged — Go allows discarding
  both return values of a statement-position call) confirmed to be the
  only two call sites in the current tree, by both the Dev and
  independently by the Reviewer.
- `app.go`'s `Run` captures `deps` from `pagesInit(...)` (a new
  `var pagesInit = pages.Init` seam — see below for why) and, in its
  existing deferred cleanup closure, drains `deps.AsyncWork` the same way
  it already drains its own `wg`, strictly before `database.Close()`
  (LIFO — the cleanup closure is registered *after* `defer database.Close()`,
  so it runs first on every return path).
- `deps` is nil-guarded in the cleanup (`pages.Init` runs late in `Run`;
  an early return before it — `plugins.Init`, `marketplace.NewCatalogRepository`,
  a listen-bind failure — leaves `deps` nil).
- `drainBackgroundServices` gained a `label string` parameter so its
  timeout log line identifies which of the two independent WaitGroups is
  still running.
- A dedicated `asyncWorkDrainTimeout = 20 * time.Second` (not a reuse of
  `backgroundDrainTimeout`'s 10s) — sized to actually cover
  `printReceiptAsync`'s own `printAsyncTimeout` (15s) plus its further 5s
  failure-write context on top, added after the first review round found
  the original 10s bound could never cover the case the drain exists for.

## What the independent review found

**First round — ACCEPT-WITH-FIXES**, one real blocker:

- **BLOCKER (AC #2 unmet)**: the reviewer mutation-tested the diff by
  deleting the `app.go` drain wiring entirely and re-running the full
  targeted suite — every existing test (including the new
  `TestInit_ReturnedDepsIsTheSameInstanceAsyncPrintGoroutinesTrack`, which
  proves `pages.Init`'s *half* of the fix) stayed green. Neither new test
  actually exercised `Run`'s own shutdown sequence, so the fix itself
  could be silently deleted with CI green — precisely the class of gap
  the card's acceptance criteria exists to close.
- **should-fix**: `backgroundDrainTimeout` (10s) reused for the AsyncWork
  drain could never cover a slow (not instantly-failing) printer's real
  write attempt.
- **should-fix**: production is now the first real caller of
  `AsyncWork.Wait()` (previously test-only), which makes `sync.WaitGroup`'s
  documented "Add concurrently with Wait" panic reachable if a
  tender/invoice handler is still running past `server.Start`'s own
  graceful-shutdown bound when the drain's `Wait` sees the counter hit
  zero. Narrow (only reachable on an already-degraded shutdown path,
  reproduced by the reviewer against a throwaway stress harness, not
  observed in this codebase) and not fixed inline — a "refuse new `Add`
  once shutdown has begun" guard is a bigger design change than this
  card's own scope (a mutex/flag across `Deps`), so it's documented as a
  known follow-up on the `AsyncWork` field instead of grinding this card
  to cover it.
- Two one-line nits: a leaked background-loop context in the new pages
  test, and a log-message substring check narrow enough to miss a timeout
  on the new (second) drain specifically.
- One accepted-as-is nit (dropped an unnecessary `net/http` import).

Everything the review verified independently and confirmed correct (not
re-litigated in the fix round): defer/LIFO ordering by hand-trace,
nil-guard correctness in both directions, no data race on `deps`
(confirmed under `-race`), the `label` parameter's consistency across all
call sites, no other `pages.Init` callers needing changes, and that
`TestInit_ReturnedDepsIsTheSameInstanceAsyncPrintGoroutinesTrack` itself
is an honest test — the reviewer mutation-tested it by swapping in a
*different* `*common.Deps` (sharing everything except its own
`AsyncWork`, the realistic regression rather than a nil-panicking bare
struct) and got 20/20 failures, then 400/400 clean passes unmutated.

**Fix round** (scoped to the blocker + one-liners, per the pipeline's
own "second round must be earned, and scoped to the fix" rule — this
qualified, a blocker was found):

- Added `TestRun_WaitsForAsyncWorkBeforeClosingDatabase` via a new
  `pagesInit` seam var (`var pagesInit = pages.Init`, swappable in
  tests without exporting `deps` from `Run`). It boots a real `Run()`
  (a genuine bind on `127.0.0.1:0`, not the existing early-bind-failure
  test's path), registers a controlled `AsyncWork` goroutine right after
  the real `pages.Init` returns, cancels `ctx` deterministically once that
  goroutine is registered (via a signal channel, not a fixed sleep — so
  the test isn't racing against however long boot happens to take), and
  asserts the goroutine's own delayed DB query succeeds after `Run`
  returns. **Verified as honest by the orchestrator**, independently of
  the Dev subagent that wrote it: re-applied the reviewer's exact
  mutation (removing the drain call, `_ = deps // MUTATION`) — the new
  test failed deterministically with `sql: database is closed` — then
  reverted the mutation and confirmed the real fix passes again.
- Sized `asyncWorkDrainTimeout` to 20s with a comment explaining the
  15s+5s worst case it needs to cover.
- Documented the `WaitGroup` misuse risk on `Deps.AsyncWork`'s own doc
  comment rather than filing a separate card — it's directly adjacent to
  the field a future reader needs to see this note on, and small enough
  not to warrant its own board entry; a fuller guard is still a real
  follow-up if this ever becomes a live symptom.
- Both one-line nits applied as suggested.

## What was verified beyond the review

- `go build ./...`, `go vet ./...`, `gofmt -l` on every changed file —
  all clean.
- `go test ./... -count=1` — full repo suite, every package green.
- `go test ./internal/app/... ./internal/pages/... -count=1 -race` — run
  independently by the Dev subagent, the Reviewer subagent, and the
  orchestrator (three separate runs, same result) — clean, no races, no
  hang (`internal/pages` alone takes ~5+ minutes under `-race`, confirmed
  clean end to end each time).
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all green (this diff touches no SQL
  outside `internal/data`/`internal/db` — it touches none at all — no
  self-order routes, no plugin-menu reads).
- No money involved, no new user-facing strings (shutdown-path only,
  `WaitForAsyncWork` is never called from a live request handler — the
  card's own non-goal, confirmed: the only new caller is `app.go`'s
  shutdown closure).
- Mutation-tested both the production wiring (blocker fix, confirmed
  failing without it) and the honesty of both new/changed tests
  personally, not just trusting subagent self-reports.

## Deferred / explicitly out of scope

- A proper "refuse `Add` once shutdown has begun" guard against the
  `WaitGroup` misuse panic (should-fix #3 above) — documented as a known
  sharp edge on `Deps.AsyncWork`, not built here; would need its own
  synchronization primitive across `Deps` and is a bigger design question
  than this card's scope.
- Unifying `Deps.AsyncWork` and `app.go`'s own `wg` into one mechanism —
  explicitly a non-goal in the card's own text; this fix wires the second,
  existing mechanism in rather than inventing a third.

## Verdict

**Safe to merge.** The blocker the first review round found (an
unenforced acceptance criterion — the fix could be silently deleted with
CI green) is closed by a test the orchestrator personally mutation-tested
against the reviewer's own reproduction steps, not just re-run. Both
should-fix findings are addressed (one fully, one documented as a
narrow, already-degraded-path follow-up per the review's own
"not a blocker" characterization). Full repo suite green, targeted
package `-race` clean across three independent runs.
