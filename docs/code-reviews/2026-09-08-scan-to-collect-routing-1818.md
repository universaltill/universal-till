# Code review: scan a receipt by order status instead of always opening refund (ut-docs#1818)

**Date:** 2026-09-08
**Card:** ut-docs#1818 (`complexity:medium`)
**Diff:** `internal/pages/pos_api.go`, `internal/pages/order_status.go`,
`web/ui/pages/order_view.html` (new), `web/locales/{en,ar,fa,tr}.json`,
`web/help/{en,ar,de,fa,tr}/order-status.md`,
`internal/pages/pos_scan_receipt_status_test.go` (new),
`internal/pages/order_view_test.go` (new)

## What shipped

`POST /api/pos/scan` used to redirect any scanned receipt-number barcode
unconditionally to `/refund/{receipt}`, which itself silently bounces to
`/journal/{receipt}` for anything that isn't a completed sale. Many receipt
scans are actually a customer returning to **collect** an order tracked via
`internal/pos.OrderStatus*` (new/preparing/ready/collected/cancelled), and
landing the operator on the refund screen for that is both the wrong
workflow and the single most dangerous screen in the app to reach by
accident.

`resolveReceiptScanDestination` (`pos_api.go`) now branches on the sale's
own state and, for a completed sale, the order's lifecycle status:

- not found / a real read failure → an honest "couldn't read this
  receipt" toast (see review finding below — this used to say "not
  completed", which was false for a plain DB hiccup)
- not a completed sale → "this sale is not completed" toast
- completed, tracked, cancelled → "this order was cancelled" toast
- completed, tracked, collected, or never tracked → `/refund/{receipt}`,
  unchanged from today
- completed, tracked, new/preparing/ready → new `GET /orders/{receipt}`
  view

The new view (`order_status.go`, template `order_view.html`) shows the
sale's lines/total/status with a one-tap **Collect** button that posts
through the *existing* `POST /api/orders/{receipt_no}/status` endpoint and
its `OrderStatusAllowed` forward-only guard — no new mutation path.

## Independent review (Opus, isolated worktree) and what it found

Full review ran build/vet/test itself, re-verified two TDD claims by
reverting the relevant production code and confirming the corresponding
test failed with a real assertion (not a compile error), then restored
and confirmed green again. Findings, all fixed in this diff:

1. **BLOCKER (fixed).** The original comment claimed order status is
   "already synced to every till via the LAN-sync journal (ADR-0079)" and
   used a purely local `repo.LatestOrderStatus` read. This was **verified
   false**: `sync_admin_repo.go` explicitly excludes `order_status_events`
   from the sync journal ("a periodic snapshot of it would actively
   misbehave"), and ADR-0079's SSE bridge only tells a replica to re-poll
   the primary — it never writes the replica's own tables. Concrete
   failure: on a two-till shop, a replica scanning a receipt for an order
   that's `ready` on the primary would read its own always-empty local
   table, see "never tracked", and land the operator on `/refund` — the
   exact outcome this card exists to prevent, working only on
   single-till shops and on the primary itself.

   **Fix:** new `latestOrderStatusForReceipt` (`order_status.go`) mirrors
   the existing `fetchOrdersFromPrimary` proxy-then-local-fallback shape
   already used by `/ui/orders` and the one-tap write: on a reachable
   replica it searches the primary's active-orders list for the receipt
   first (an unambiguous hit — an active order can only be
   new/preparing/ready); a miss falls through to the local read exactly
   as the primary/standalone path always has. Both scan-routing and the
   new page use this helper, not a direct repo call.

   **Verified by a new test**
   (`TestScanHandler_ReceiptRouting_ReplicaUsesPrimaryOrderStatus`): seeds
   the sale locally but applies the order-status change *only* on a fake
   primary server, never locally, and confirms the scan still routes to
   the order view. Reverting the fix (reading `repo.LatestOrderStatus`
   directly) makes this test fail.

   **Accepted, documented residual gap:** a replica still cannot
   distinguish collected/cancelled/never-tracked/too-old for an order
   that *isn't* currently on the primary's active list (that list only
   carries non-terminal orders, and is itself bounded to the 50 most
   recent). All of those cases already resolved to "refund" (or an empty
   local read) before this card, so the fix can only make routing more
   correct, never less safe — it just doesn't fully close every replica
   edge case. Documented in the helper's own comment rather than
   silently accepted.

2. **MAJOR (fixed).** The original diff had a branch on
   `detail.Status == "refunded"` plus a `pos.toast.receipt_already_refunded`
   key and a help-docs sentence describing it. **Verified unreachable**:
   grepped every production write path and confirmed a refund never sets
   `sales.status` to `"refunded"` — it inserts a separate
   `sale_type='return'` row (`refund_page.go`). The reviewer additionally
   drove a real full refund through `POST /api/refund` and read the
   original sale's status back as `"completed"`, confirming this
   empirically, not just by inspection. The only test exercising this
   branch seeded `status='refunded'` directly via SQL — a state the
   application itself never produces, so it proved nothing about real
   behavior.

   **Fix:** removed the branch, the `receipt_already_refunded` key (all 4
   `web/locales/*.json`), the SQL-seeded test case, and the "already
   refunded" clause from all 5 `web/help/*/order-status.md` files. Added
   `pos.toast.receipt_read_error` for the genuine DB-read-failure case
   (see finding 3).

3. **MINOR (fixed).** `GetSaleDetail`'s error path returned
   `pos.toast.receipt_not_completed` ("this sale is not completed") on a
   transient DB error — a definite, false claim about a sale whose status
   was never actually read. Split into its own honest
   `pos.toast.receipt_read_error` key ("couldn't read this receipt — try
   scanning it again"), `"error"` toast level; the two genuine status-fact
   toasts (cancelled, not-completed) stay `"info"`.

4. **MINOR (fixed).** `order_view.html`'s `data-status` attribute carried
   `.Sale.Status` (always `"completed"` on this page, by the handler's own
   guard) instead of the *order* status the label text next to it
   describes — inconsistent with `orders_list.html` and
   `writeOrderStatusFragment`, both of which carry order status in that
   attribute. Latent (nothing reads `data-status` today) but would
   mis-style the instant a CSS rule keyed off it landed. Fixed by passing
   `Status` (the order status) as its own template value.

5. **MINOR (fixed).** The Collect button used
   `hx-target="this" hx-swap="outerHTML"`, so tapping it replaced only the
   button — the header status line above it stayed stale ("Preparing")
   even after the tap succeeded and the button itself now read
   "Collected". Fixed to target `#order-status-line` with
   `hx-swap="innerHTML"`, the exact pattern `orders_list.html`'s own
   one-tap buttons already use against their status cell — so the header,
   button area and refund link all stay consistent, and the change is a
   straight reuse of an established, already-proven pattern rather than a
   new one.

6. **MINOR (fixed).** The page's not-found and not-completed cases both
   redirected to `/journal/{receipt}` — but `/journal/{receipt}` answers a
   raw, unthemed `http.NotFound` for an unknown receipt
   (`journal_page.go`), a dead end. `/refund/{receipt}`'s own handler
   already splits this correctly (not-found → `/journal` the list;
   not-completed → `/journal/{receipt}`, a real page there). Matched that
   split exactly.

7. **NITs (fixed/noted).** Toast-level consistency (addressed as part of
   finding 3). `web/help/{ar,fa,tr}/order-status.md` have no `keywords:`
   front-matter field at all (pre-existing gap, not introduced here — left
   alone). `/orders/` with an empty receipt segment redirects harmlessly to
   `/journal/`; untested, low-value to add a case for.

## What was verified beyond automated tests

- **TDD, independently re-verified by the reviewer**: reverted
  `resolveReceiptScanDestination` to the old unconditional-refund
  behavior → 4/6 `TestScanHandler_ReceiptRouting` subtests failed with
  real assertion mismatches; reverted the terminal-status switch in
  `GET /orders/{receipt}` → both redirect tests failed
  (`status = 200, want 303`). Both restored and green again.
- **Real running app**, driven twice (before and after the review fixes)
  via `go run .` against a real SQLite file, `UT_AUTH=off`: seeded sales
  directly, drove the real `/api/pos/scan`, `/api/orders/{receipt}/status`
  and `GET /orders/{receipt}` endpoints with `curl`, and confirmed:
  uncollected → order view; collected → refund; re-scanning after a real
  Collect tap correctly now goes to refund. Screenshots taken and read
  (not just asserted-on) at 1280×900 in English (with and without line
  items — the empty-lines state renders cleanly) and in `fa`/RTL (full
  mirrored layout — sidebar, table columns, button order — nothing
  overlapping, wrapping, or cut off). After the review fixes, re-confirmed
  the corrected `hx-target`/`data-status` render correctly in the raw HTML
  of a freshly seeded uncollected order on the same real server.
- **Not independently re-verified in a browser**: dark theme (the page
  introduces zero new CSS, reusing only already-shipped `.card`/`.btn`/
  `.table`/`.muted` classes exercised elsewhere, so risk is low but this
  is an explicit gap, not a silent one) and the external
  `ut-plugin-language-{de,es}` packs' own translations of the new keys
  (core `en.json`/`ar.json`/`fa.json`/`tr.json` are complete and
  guard-verified; the packs are updated separately, see below).
- **Guards**: `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-htmx-loaded.sh`, `guard-page-http-error.sh`,
  `guard-compliance-claims.sh`, `guard-data-access.sh` — all green, run
  both before and after the review fixes.
- **Full gate**: `go build ./...`, `go vet ./...`, `go test ./...` (full
  suite, 51 packages, all green) — run both before and after the review
  fixes.
- No real client/shop name or secret-shaped literal anywhere in the diff
  (test data is `R-0001`/`R-SCAN-*`/`R-X1`/`Apple`/`Flat White`/`u-alice`/
  `u-test`).

## Non-goals confirmed untouched

- The existing `POST /api/orders/{receipt_no}/status` endpoint,
  `OrderStatusAllowed`, and the primary-proxy write path — reused, not
  modified.
- Companion card ut-docs#1833 (voucher barcode scanning) — found during BA
  to depend on ut-docs#1832 (voucher redeem UI), which hasn't landed;
  demoted to Backlog with the dependency documented on the issue, not
  touched by this diff.
- Money/tax handling — the new page only displays pre-computed
  `SaleDetail` fields via the existing `money` template func; the Collect
  button only ever posts a fixed `status=collected` value.

## Deferred / out of scope

- The replica active-list gap for a not-currently-active order (see
  blocker fix above) — documented as an accepted limitation, not a new
  card, since it was never better than this before ut-docs#1818.
- Dark-theme visual re-check — flagged above as not independently
  re-verified rather than silently skipped.

## Language pack follow-up (lang-pack-drift, ut-docs#1576)

Per the lane-ownership "work with no card" rule, the lane merging this
core change owns pushing the same 4 new `pos.toast.*`/`orders.view.*` keys
into `ut-plugin-language-de` and `ut-plugin-language-es` before/alongside
this merge, so `main` doesn't go red under `lang-pack-drift`.

## Verdict

**Safe to merge** after the fixes above (all applied in this diff, not
deferred). No blockers remain.
