# Orders board: terminal orders (collected/cancelled) leave the active queue

**Card:** universaltill/ut-docs#1389 — "Collected and cancelled orders remain on the active Orders board"
**Complexity:** medium (Dev: Sonnet inline; Review: Opus, independent worktree-isolated subagent)
**Branch:** `fix/1389-orders-board-terminal-filter`

## What shipped

`/orders` is meant to be the *active* work queue, but an order stayed
listed forever once it reached a terminal lifecycle state:

- `internal/data/order_status_repo.go` — `ListRecentOrders` (the ONE query
  shared by the local `/ui/orders` fragment, the primary's
  `GET /api/sync/orders` a replica polls, and the offline local fallback)
  now excludes `order_status IN ('collected', 'cancelled')`. One fix,
  every board that reads it.
- `internal/pos/order_status.go` — new `IsTerminalOrderStatus(status string) bool`,
  colocated with the rest of the status vocabulary/conflict rule.
- `internal/pages/order_status.go` — the one-tap POST response now also
  carries an htmx out-of-band delete (`<div id="order-row-{receiptNo}"
  hx-swap-oob="delete"></div>`) whenever the resulting status is terminal,
  so the row disappears immediately — no full-page reload, no waiting for
  the 15s poll. Both the local-write and replica-proxied-to-primary code
  paths go through the same `writeOrderStatusFragment`, so both get it.
- `web/ui/partials/orders_list.html` — each row gets `id="order-row-{{.ReceiptNo}}"`
  as the OOB delete's target.
- `web/help/en/order-status.md` — documents the new behavior; screenshots
  regenerated (`make docs-shots`).

No new user-facing strings (the OOB marker carries no text), no money
touched, no schema change.

## A real bug the driven browser run caught (not just the Go tests)

The first implementation used `<tr id="order-row-{receiptNo}" hx-swap-oob="delete"></tr>`
as the OOB element. Every Go-level `httptest` assertion passed — they only
check the response *string* contains the marker — but it silently did
nothing in a real browser: htmx's fragment parser
(`web/public/vendor/htmx.min.js`) only wraps the response in `<table>`
context when the response's **first tag** is table-related; here the
response starts with the `<span class="order-status">` fragment, so the
bare `<tr>` gets parsed "in body" instead, where the HTML5 parsing
algorithm silently drops an out-of-context `<tr>` before htmx's own JS
ever reads `hx-swap-oob`.

Caught by driving a real sale to completion in Chromium
(`e2e/tests/orders-terminal-row-removal-1389.spec.ts`) and reading the
live DOM, per the Tester skill's "look at it, don't just assert on it."
Fixed by switching the OOB element to a `<div>` — the swap target is
matched purely by `id`, so the element's own tag doesn't matter (and here
must not be `<tr>`).

## Independent review (Opus, worktree-isolated, 2026-09-01)

**Verdict: safe to merge, no blocking findings.**

Independently verified rather than taken on trust:

- Read `htmx.min.js`'s fragment-parsing logic personally and confirmed the
  `<tr>`-drop account is accurate (`useTemplateFragments` is off by
  default; the first-tag `<table>`-context switch misses a response
  starting with `<span>`).
- **Adversarial revert-then-restore #1:** reverted the `<div>` fix back to
  `<tr>` and re-ran the e2e spec for real — it failed with the row still
  present (`Received: 1`, want `0`), proving the spec isn't a tautology.
  Restored, re-ran green.
- **Adversarial revert-then-restore #2:** removed the `AND order_status
  NOT IN (...)` clause and re-ran `TestListRecentOrders_ExcludesTerminalStates`
  — real assertion failure (6 orders instead of 4), not a compile error.
  Restored, re-ran green under `-race`.
- **Extra tautology probe:** inverted `IsTerminalOrderStatus`'s call site
  — all four new handler tests failed, including the "non-terminal move
  must not remove the row" test. Not tautological.
- Confirmed by reading the code (not the comment) that `sync_orders.go`'s
  `GET /api/sync/orders` and `order_status.go`'s local fallback both call
  the exact same `ListRecentOrders` — one fix covers the primary, the
  replica proxy, and the offline fallback.
- Ruled out a `NULL NOT IN (...)` silent-exclusion trap: `order_status` is
  `TEXT NOT NULL DEFAULT ''` (migration 033), confirmed, not assumed.
- Ran `EXPLAIN QUERY PLAN` before/after against a real migrated DB: the
  extra predicate produces an identical query plan (same index scan, same
  pre-existing full sort) — no performance regression.
- Confirmed the `receiptNo` now echoed into the row `id` attribute goes
  through `template.HTMLEscapeString` — no new XSS surface.
- Confirmed both call sites of the now-6-arg `writeOrderStatusFragment`
  pass `receiptNo` in the right position (arity alone wouldn't catch a
  same-type transposition; checked by eye + the e2e spec's real DOM
  assertion).

Non-blocking notes recorded (not fixed — genuinely deferred, not silently
dropped):

1. `order_status_repo.go`'s SQL literal list (`'collected','cancelled'`)
   duplicates the vocabulary `pos.IsTerminalOrderStatus` owns, joined only
   by a comment — structurally forced (`internal/data` can't import
   `internal/pos` without a cycle the other way already exists). A future
   third terminal state would drift silently. A cheap drift-guard test
   (assert `pos.OrderStatuses` filtered by `IsTerminalOrderStatus` equals
   exactly `{collected, cancelled}`) would pin it — left as a follow-up,
   not blocking.
2. No direct unit test for `pos.IsTerminalOrderStatus` itself (only
   covered transitively via the handler tests) — low value given existing
   coverage.
3. Removing the last active row leaves an empty `<table>` with headers
   until the next 15s poll re-renders the `orders.empty` state. Cosmetic.
4. A replica that collected an order via the primary can see that order
   **reappear** if the primary later becomes unreachable and it falls back
   to local state (which never saw the write) — a new instance of the
   already-documented ut-docs#1350 offline-write limitation, not
   introduced by this diff.
5. The row id embeds `receipt_no` (which can carry an operator-set
   `sync.receipt_prefix`); an exotic prefix could in principle produce an
   invalid CSS id selector for htmx's `"#"+id` lookup. Pre-existing (the
   template already keyed `hx-target` off the same value) and unchanged in
   kind by this diff.

## Verified beyond automated tests

- Real Chromium run (`orders-terminal-row-removal-1389.spec.ts`): rings up
  a real sale (barcode `5000000000012`, demo catalog), tenders cash, taps
  **Collected** for real, asserts the row leaves the live DOM immediately
  (`toHaveCount(0)`, no manual sleep) and stays gone after a full page
  reload (proving the server-side filter, not just the client swap).
  Passed.
- Pre-existing `sale-rail-orders-1349.spec.ts` (also touches `/orders`)
  re-run for regression — unaffected, all 3 tests pass.
- `EXPLAIN QUERY PLAN` diff (reviewer) confirmed no query-plan regression.

## Gate

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/data/... ./internal/pages/... ./internal/pos/...`
  (targeted to changed packages, `-run` scoped to the relevant tests,
  `-race`) — all green, including the reviewer's own from-scratch run in
  an isolated worktree.
- `scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-compliance-claims.sh`,
  `guard-e2e-fixtures-import.sh`, `guard-htmx-loaded.sh`, and the rest of
  the `build` job's guard list — all pass.

**Known, pre-existing, unrelated environment limitation (not this diff):**
the *whole* `internal/data` and `internal/pages` packages, run in full
under `-race`, exceed a 20-25 minute timeout in this sandboxed pipeline
environment — confirmed independently by both Dev/Tester and the Reviewer,
and reproduced on tests with zero relation to order status (a legacy
plugin-install-endpoint test, a sync-admin-chip test, an import-rollback
test), each doing a from-scratch SQLite migration under heavy `-race`
instrumentation. Every test actually touched by this change passes
individually and quickly, `-race` included, and the reviewer's own
`-race`-free full run of all three packages is green. This matches the
already-tracked ut-docs#1366 ("internal/data's full test suite exceeds the
default go test timeout under -race") — now apparently also true of
`internal/pages` — logged as a new observation rather than silently
worked around.

## Safe-to-merge verdict

Yes. No blocking findings from the independent review; all acceptance
criteria met and verified; the one real bug found (the `<tr>`/htmx
parsing gap) was caught and fixed before merge, with adversarial
regression coverage proving both the fix and the test that guards it.
