# Code review — ut-docs#514: drain AsyncWork before DB close in internal/pages test helpers

Date: 2026-08-20
Reviewer: Sonnet (independent fresh-context subagent) — different instance from the
implementer, never saw the dev reasoning (the `complexity:easy` review lane).
Branch: `fix/514-async-cleanup-ordering-pages-tests` — PR universaltill/universal-till#<TBD>
Implementer: Sonnet (pipeline cycle)

## Context

Follow-up to ut-docs#425, which fixed a flaky `t.TempDir()` cleanup race in
`internal/pages/self_order_shop_test.go`: the detached goroutines `completeTender`
fires (`printReceiptAsync`, `printKitchenAsync`, invoice print) keep touching
`Deps.Db`/`Deps.Settings` after a handler has responded. If a test-DB helper's
`t.Cleanup` closes the DB before those goroutines finish, they run against a closed
handle — "sql: database is closed" log noise at best, a `RemoveAll`-vs-SQLite-sidecar
"directory not empty" flake at worst. #425 wired `common.Deps.AsyncWork` +
`WaitForAsyncWork()` into `setupSelfOrderShopDeps` only. #514 applies the same
cleanup-ordering fix to the remaining test helpers built over `openPagesTestDB`
that exercise `completeTender`/refund.

## What shipped

Purely cleanup-**ordering** changes — no test assertion touched. Go runs `t.Cleanup`
functions LIFO, so registering `t.Cleanup(dp.WaitForAsyncWork)` (or `defer`-ing it)
*after* the existing DB-close cleanup makes the drain run *first*.

- **`pos_api_test.go`** (`newPOSTestDeps`) — drain registered after `db.Close`.
- **`refund_page_test.go`** (`newRefundTestDeps`) — drain registered after `db.Close`.
- **`invoice_page_test.go`** (`newInvoiceTestDeps`) — drain registered after `db.Close`.
- **`sync_sales_test.go`** (`newSyncSalesTestDeps`) — drain registered after `db.Close`;
  this helper backs the two-till / replica stock-ownership tests
  (`stock_ownership_test.go`), which do drive `completeTender` through the primary till.
- **`journal_test.go`** (`TestOfflineTenderUpdatesJournal`, inline Deps) — uses
  `defer db.Close()`, so `defer dp.WaitForAsyncWork()` added after it drains first.
- **`stock_ownership_test.go`** — has no helper of its own; it constructs Deps via
  `newPOSTestDeps` and `newSyncSalesTestDeps`, both fixed above, so it is covered
  transitively (the two listed test files that share the exposure).

### Regression test

`internal/pages/async_cleanup_ordering_test.go` — `TestTestDepsHelpers_DrainAsyncWorkBeforeClosingDB`.
For each helper, it builds a Deps inside a nested sub-test, registers a real
`AsyncWork` goroutine that sleeps then queries `SELECT 1` through `dp.Db`, and after
the sub-test's cleanup runs asserts the goroutine (a) ran and (b) did not hit a closed
DB. It **failed on all four un-fixed helpers before the fix** (and passed for the
already-fixed `setupSelfOrderShopDeps` as a control), and passes for all five after.
Cross-goroutine reads of `queryErr`/`ran` are mutex-guarded.

## Verification beyond automated tests

- `go test ./internal/pages/ -run TestTestDepsHelpers_DrainAsyncWorkBeforeClosingDB`
  — fails pre-fix (all 4 unfixed helpers), passes post-fix (control + 4).
- `go test ./internal/pages/ -race -count=20 -run '<affected tenders>'` — clean,
  `ok 137.256s`, no "database is closed", no DATA RACE. (Deliberately scoped away from
  the 4097-round-trip `TestAskTaxRateBP_OverflowAndConcurrency`, ut-docs#648, which
  sits at the -race package timeout.)
- `guard-data-access.sh`, `guard-kiosk-engine.sh`, `gofmt -l` — all clean.

## Findings

<!-- filled in from the independent review below -->

## Non-goals (unchanged from the card)

Scoped specifically to the `printReceiptAsync`/`printKitchenAsync`/invoice-print
goroutine class ut-docs#425 already tracks via `AsyncWork` — not a hunt for other
detached-goroutine classes.
