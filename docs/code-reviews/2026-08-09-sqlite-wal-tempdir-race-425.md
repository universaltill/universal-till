# Code review: join async print/kitchen/invoice-print goroutines before Db close

**Card:** universaltill/ut-docs#425
**Date:** 2026-08-09
**Complexity:** medium — Dev inline (Sonnet), Review via an independent Opus
subagent (isolated worktree). One review round: it found one blocking
finding (a false-pass regression test), fixed inline; nothing money/tax/
data-loss/security class, so a second independent round wasn't earned per
this pipeline's process-depth rule.

## What shipped

`TestSelfOrderShop_CheckoutAppliesPluginReportedTipFromAuthorizeResponse`
(`internal/pages/self_order_shop_test.go`) failed once in CI with
`t.TempDir()` cleanup error `unlinkat ... directory not empty`. Root cause,
found by reproducing it live (`go test -race -run
TestSelfOrderShop_CheckoutAppliesPluginReportedTipFromAuthorizeResponse
-count=40 -v`, ~800 `sql: database is closed` errors from
`data.settings.get`, traced to source via a temporary stack-dump patch to
`internal/data/repo_observability.go`, reverted before commit):
`completeTender` (`internal/pages/pos_api.go`) fires two detached,
unjoined goroutines after every tender — `printReceiptAsync`
(`internal/pages/print_api.go`) and `printKitchenAsync`
(`internal/pages/kitchen_print.go`) — each reading `printer.*` settings
(and, on failure, writing an audit row) through `d.Db`, with no caller
left holding a handle to them. That's correct for checkout itself
(offline-first: never block on printer I/O), but a test that closes `Db`
and removes its `t.TempDir()` right after the HTTP response returns can
race that still-in-flight goroutine — `t.TempDir()`'s `RemoveAll` racing
SQLite's WAL sidecar files (`journal_mode=WAL`, `internal/db/db.go`) is
the mechanism behind the "directory not empty" failure.

A third, structurally identical detached goroutine
(`internal/pages/invoice_page.go`'s manual thermal-print-on-issue path)
was found by the independent review and folded into the same fix for
consistency, since it's the same class of gap and a one-line change.

Fix:

- **`internal/pages/common/deps.go`**: adds `AsyncWork sync.WaitGroup` +
  `WaitForAsyncWork()` to `Deps`, the same join-before-teardown shape
  already used for `WasmRuntime`'s drainer goroutines (ut-docs#380).
- **`internal/pages/print_api.go`, `internal/pages/kitchen_print.go`,
  `internal/pages/invoice_page.go`**: each async-print call site now
  `d.AsyncWork.Add(1)` immediately before `go func()` and
  `defer d.AsyncWork.Done()` as the goroutine's first statement.
- **`internal/pages/self_order_shop_test.go`**: `setupSelfOrderShopDeps`
  registers `t.Cleanup(dp.WaitForAsyncWork)` *after* registering
  `t.Cleanup(func(){ d.Close() })` — `t.Cleanup` runs LIFO, so at test end
  `WaitForAsyncWork()` now runs before `Close()`, before `t.TempDir()`'s
  own removal.
- **`internal/pages/print_api_test.go`**: new regression test,
  `TestAsyncPrintGoroutinesFinishBeforeWaitForAsyncWorkReturns`.

## Independent review (Opus, isolated worktree)

Ran the full gate itself (`go build`, `go vet`, targeted `-race -count=20`
runs, a full `go test ./...`), did a real revert-then-restore TDD check on
both the production fix and the new test, traced every `Add`/`Done` pairing
by hand, grepped every `WaitForAsyncWork` call site to confirm none is on
a live request path, and checked all five relevant `CLAUDE.md` guards.

**Verdict at first pass: do not merge.** One blocking finding.

### Finding — HIGH (false-pass test), fixed

The first draft of `TestAsyncPrintGoroutinesFinishBeforeWaitForAsyncWorkReturns`
asserted only that `d.Close()` and `os.RemoveAll(dir)` returned `nil`
immediately after `WaitForAsyncWork()`, looped 50x. The reviewer proved
this was a false pass: reverting *only* `print_api.go`/`kitchen_print.go`
(keeping `deps.go`'s `AsyncWork` field so it still compiled, making
`Wait()` a permanent no-op) and running the test 300 iterations under
`-race` — it **passed every single time**, while logging ~6000
`database is closed` errors. `Close()`/`RemoveAll()` almost always
succeed regardless of the race; the actual "directory not empty" failure
is a much rarer sub-case, so asserting on the filesystem symptom instead
of the goroutine's own effect gave false confidence — a test that would
stay green if someone deleted the `Add(1)`/`Done()` lines tomorrow.

Fixed by asserting on an effect the goroutines actually produce instead
of inferring completion from a rare timing window: the test now
configures both printers pointing at a receipt number that doesn't exist,
so `buildReceiptDoc`/`buildKitchenTicket` fail on the missing sale lookup
before any network I/O, guaranteeing both goroutines write a
`print_failed`/`kitchen_print_failed` audit row. It asserts both rows are
present the instant `WaitForAsyncWork()` returns — no polling, no retry,
no `-race`/high-iteration-count dependency. Verified in both directions
in this session (not just claimed): reverting the `Add`/`Done` wiring
makes it fail deterministically on iteration 0 (`got 0` audit rows);
restoring passes deterministically, 10x under `-race` at ~2.5s/run (down
from the old test's ~41s/run for equivalent confidence).

### Design sanity check — no blocking issue

`Add(1)` is unconditionally the statement immediately before `go func()`
at all three call sites; `defer d.AsyncWork.Done()` is the first statement
inside each, so no early-return path can skip `Add` and a panic inside
still decrements. Both print paths are bounded by their existing 15s
`context.WithTimeout`, so `Wait()` can never block indefinitely. The
classic `Add`-racing-`Wait`-at-zero `sync.WaitGroup` misuse isn't reachable
today — the only two callers of `WaitForAsyncWork` are test cleanups,
confirmed by grep, never a live request path.

### Deferred, not fixed here — two follow-up cards filed

- **Production graceful shutdown has the same class of gap, still open.**
  `internal/app/app.go` already has its own `wg`, added for exactly this
  reason ("so Run can wait for them to actually exit before its deferred
  database.Close()", per that code's own comment, from a 2026-07-30
  mobile-shutdown CI flake). `Deps.AsyncWork` is a second, disconnected
  WaitGroup that `drainBackgroundServices` doesn't know about, and nothing
  in production calls `WaitForAsyncWork` today — a real print goroutine can
  still be mid-flight when `database.Close()` runs at real shutdown, the
  production instance of a class this codebase already fixed once
  elsewhere. Filed as a new Backlog card rather than folded into this
  diff — wiring it into shutdown is a different, riskier change (touches
  live shutdown ordering) than the test-only fix this card scoped.
- **The same latent exposure exists in several other test files** that
  exercise `completeTender`/refund through a *different*, non-WAL test DB
  helper (`openPagesTestDB`, `internal/pages/ui_smoke_test.go`) —
  `pos_api_test.go`, `refund_page_test.go`, `stock_ownership_test.go`,
  `journal_test.go`, `invoice_page_test.go`. Lower severity (no WAL
  sidecar files in play, so no "directory not empty" mechanism), but the
  same "goroutine still touching a closed/being-removed db" shape. Filed
  as a new Backlog card — package-wide, mechanical, but a separate
  reviewable change from this one.

## Verified beyond the automated suite

- **Live reproduction of the original bug**, not just trusted from the
  ticket: `go test -race -run
  TestSelfOrderShop_CheckoutAppliesPluginReportedTipFromAuthorizeResponse
  -count=40 -v` on pre-fix code produced ~800 `database is closed` lines;
  a temporary stack-dump patch (reverted, never committed) traced the
  actual goroutine to `printReceiptAsync`/`printKitchenAsync` via
  `printerConfig` → `settings.Store.Get`.
- **Root-cause hypothesis testing, including one dead end reported
  honestly**: an earlier hypothesis (missing `PRAGMA wal_checkpoint`
  before `sql.DB.Close()`) was tested with a dedicated stress harness
  (2000+ open/write/close/removeall iterations, then 900 more with 8
  concurrent goroutines) and did **not** reproduce the race — ruled out
  before the real cause (the detached print goroutines) was found via
  `-race` plus a stack-dump trace.
- **Revert-then-restore TDD, both directions, personally**: reverted
  `print_api.go`/`kitchen_print.go` (keeping `deps.go`), confirmed the new
  test fails deterministically with the exact claimed symptom (`want 2
  failure-audit rows ... got 0`); restored, confirmed 10x pass under
  `-race`. The independent reviewer separately reverted the same way and
  ran 300 iterations against the *original* (since-replaced) test draft,
  which is what surfaced the false-pass finding above.
- **The two recurring bug classes this pipeline keeps re-finding**:
  confirmed not applicable — no file-write handler in this diff (the new
  test's only directory creation is `os.MkdirTemp`, self-creating); no
  cwd-relative path where `paths.Data(...)` belongs (production's logo
  path was already correct and untouched; the new test uses
  `os.MkdirTemp("", ...)`, an absolute OS temp path).
- **Offline-first**: confirmed by grep — exactly two call sites of
  `WaitForAsyncWork`, both `_test.go`, never a request-handling path.
  `completeTender` still returns without waiting.
- No raw SQL added outside `internal/data`/`internal/db` (none added at
  all); no `money.Money` involved; no user-facing strings (backend
  concurrency plumbing — no template, route, or locale-key change); no
  ADR needed, this extends an existing pattern rather than introducing a
  new architectural decision.
- No real client/shop name anywhere in the diff (test fixtures are
  `"receipt-1"`/`"receipt-does-not-exist"`/`""`; the pre-existing `"Task
  Runner Cafe"` fixture is untouched); no secret-shaped literal.
- Full `go build ./...`, `go vet ./...`, full `go test ./...` (all
  packages, zero failures) — run by Dev, by the reviewer in its isolated
  worktree, and once more here after folding the reviewer's fix in.
  `guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-help-topics.sh` all pass.

## Safe-to-merge verdict

Yes, with the reviewer's false-pass-test finding fixed (the deterministic
audit-row assertion described above) and the one-line `invoice_page.go`
consistency fix folded in. No manual/`web/help/` update needed — backend
goroutine-lifecycle plumbing with no shop-owner-visible surface (no route,
template, or observable runtime behavior change for a real till);
confirmed explicitly, not skipped. Two legitimate follow-ups (production
shutdown wiring, package-wide test-helper exposure) tracked as new
Backlog cards rather than expanded into this diff.
