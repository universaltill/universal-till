# Order lifecycle status backbone (ut-docs#526)

**Date:** 2026-08-09
**Author (Dev):** Fable subagent, `complexity:hard`
**Reviewer:** Opus subagent, fresh context, isolated worktree
**Verdict:** Safe to merge, with 3 blocking findings fixed before this record was written.

## What shipped

A central `new → preparing → ready → collected` (+ terminal `cancelled`)
order status, deliberately scoped as the shared backbone three future
cards (#516 KDS, #517 live order view, #528 pager, #527 customer
tracking) will build on — none of those surfaces were built here.

- **Model** (`internal/pos/order_status.go`): the status vocabulary, a
  named `OrderStatusAllowed(current, next)` conflict rule, and a real
  in-process pub/sub `OrderStatusBroadcaster` for future subscribers.
- **Persistence** (`internal/db/migrations/033_order_status.sql`,
  `internal/data/order_status_repo.go`): `sales.order_status` /
  `order_status_updated_at` current-state columns + an append-only
  `order_status_events` journal, written in one transaction via
  `ApplyOrderStatus`.
- **Surface** (`internal/pages/order_status.go`,
  `web/ui/pages/orders.html`, `web/ui/partials/orders_list.html`): a
  deliberately minimal `/orders` list with one-tap status buttons and
  `POST /api/orders/{receipt_no}/status`, no manager gate (same floor-work
  reasoning as kitchen-ticket printing).
- **Conflict rule**: status only ever moves forward on the
  new(1)<preparing(2)<ready(3)<collected(4) ladder; a stale/backward
  write is a silent 200 no-op — never an error, never a journal entry,
  never a broadcast, never a visible regression. `cancelled` is terminal,
  reachable from any non-collected state. This extends ADR-0011's
  existing "conflict rules — fixed, simple" philosophy to a new
  contended-data category; no new ADR was written (a short ADR-0011
  amendment is a fair follow-up, not required to ship this).
- i18n: 16 new keys in all four in-repo locales (en/ar/fa/tr) plus the
  two external language packs (`ut-plugin-language-de`,
  `ut-plugin-language-es` — see below). Help topic
  `web/help/{en,ar,fa,tr}/order-status.md`, `routes: [/orders]`.

## Independent review — what was actually checked, not just read

- **Full gate**: `go build`, `go vet`, `go test ./...` (36 packages),
  and all repo guards (`guard-data-access`, `guard-i18n`,
  `guard-kiosk-engine`, `guard-help-topics`, `guard-plugin-menu-read`,
  plus `guard-htmx-loaded`, `guard-android-i18n`, `check-brand-assets`,
  `guard-autofill-suppression`, `guard-emoji-font`) — all green.
- **TDD claim re-verified independently, not trusted**: the reviewer
  broke `OrderStatusAllowed` (forced `return true`), re-ran
  `TestOrderStatusAllowed` and `TestOrderStatusPost_StaleBackwardMoveSilentlyDropped`,
  confirmed real assertion failures (not compile errors), then restored
  the guard and confirmed both pass again.
- **Concurrency claims verified, not assumed**: traced `_txlock=immediate`
  through `modernc.org/sqlite` to confirm `BeginTx` really issues `BEGIN
  IMMEDIATE`; stress-tested `ApplyOrderStatus` with 8–24 concurrent
  goroutines racing the same receipt under `-race` — exactly one applied
  write per forward move, journal always agreeing with current state.
  Stress-tested the broadcaster (8 publishers × 5000 events vs. 8×2000
  subscribe/drain/cancel cycles, `-race`, `-count=3`) — no send-on-closed
  panic, confirming `Subscribe`/`Publish`/`cancel` sharing one mutex is
  actually sufficient.
- **Scope creep check**: no KDS page, no pager code, no customer QR page
  — #516/#517/#528/#527 appear only in comments, as intended.
- **i18n quality**: ar/fa/tr strings read individually, not just
  key-presence-checked — idiomatic, no placeholders.
- **Manual**: help topic accurately describes what shipped; `routes:
  [/orders]` correct; `guard-help-topics.sh` (route coverage) passes.
- **Driven run** (Tester step, before review): built and ran the app
  (`UT_AUTH=off`, fresh data dir, real demo-seeded DB), seeded real sales,
  drove the one-tap endpoint over HTTP, confirmed the conflict rule live
  (a stale "preparing" tap on a "ready" order stayed "Ready", 200, no
  regression) — not just in the test suite. Screenshotted `/orders` in
  light, dark (app-level theme setting, unaffected by this change — same
  as every other page), and `fa` (RTL, confirmed `dir="rtl"`, correct
  mirrored layout, real Persian labels).

## Findings — fixed before merge

1. **BLOCKING — `ListRecentOrders` leaked non-completed sales.** The
   query filtered `sale_type = 'sale'` but not `status = 'completed'`,
   so voided/refunded/parked/still-open sales appeared in the kitchen
   list with live one-tap buttons — `ApplyOrderStatus` would happily mark
   a voided sale "preparing". Every other sales query in `pos_repo.go`
   pairs the two filters; this one didn't. **Fixed**: added `AND status
   = 'completed'`, with a TDD regression test
   (`TestListRecentOrders`, extended) confirmed failing against the bug
   (6 rows including all 4 non-completed statuses) and passing after the
   fix (2 rows).
2. **BLOCKING — `guard-docs-shots.sh` failing.** The new `/orders` help
   topic had no screenshot. **Fixed**: ran the docs-shots Playwright spec
   scoped to the new topic (`-g "order-status"`) and regenerated
   `web/help/img/manifest.json`. Guard now passes (16 routed topics × 4
   locales, fresh).
3. **BLOCKING — `check-lang-pack-drift.sh` failing.** The 16 new keys
   were missing from the sibling `ut-plugin-language-de` and
   `ut-plugin-language-es` repos, which this script fetches and checks
   against on every push to `universal-till`. **Fixed**: opened
   universaltill/ut-plugin-language-de#16 (German translations for all
   16 keys; `orders.col.status` added to the same-as-English allowlist —
   it's a standard German loanword) and
   universaltill/ut-plugin-language-es#13 (Spanish translations for all
   16 keys). Both packs' own `check-key-drift.sh` pass locally against
   core's `en.json`; their CI currently red-Xs because it fetches core's
   *live* `main`, which doesn't have these keys until this PR merges —
   expected, sequencing-dependent, not a defect (explained on both PRs).

## Non-blocking, accepted as-is (follow-up candidates, not filed as
new cards — small enough to fold into #516 when it lands)

- The one-tap POST fragment shows `status · who · when`, but the list's
  own render (`orders_list.html`) shows only `status · when` — the actor
  disappears on a page reload. Cosmetic; the journal itself always has
  the actor.
- `orders.err.bad_status`/`orders.err.not_found` are real, tested, but
  effectively unreachable from the UI today: htmx's global
  `htmx:responseError` handler intercepts non-2xx responses before a
  target swap, so the specific localized message never renders — the
  user sees the generic server-error alert instead. Both are edge paths
  the placeholder buttons never trigger in normal use.
- The POST fragment's raw RFC3339 timestamp (`2026-08-09T16:53:31Z`)
  isn't bidi-isolated, so it visually scrambles inline with translated
  RTL text (confirmed in a driven `fa` screenshot). The existing
  `journal.html` list already renders raw timestamps the same way, so
  this isn't a new deviation for the *list*; the POST response is the
  one genuinely new construct. One-attribute fix (`dir="ltr"` or bidi
  isolates) when #516 replaces this placeholder surface.
- `LatestOrderStatus` reads outside `ApplyOrderStatus`'s transaction —
  intentional latest-state semantics (a fragment can show a newer status
  than the one just written under concurrency), not a bug, but worth a
  code comment next time this file is touched.

## Verified beyond automated tests

- Real HTTP driven run against a real built binary + real SQLite DB
  (not `httptest` alone).
- Conflict rule proven live, not just asserted in a unit test.
- Concurrency safety of both the transactional write-guard and the
  broadcaster proven under `-race` with real contention, not asserted
  from reading the code.
- No real client/shop name, no literal secrets.

## Deferred (explicitly, not silently)

- The actual KDS display, pager integration, and customer QR page —
  separate cards (#516/#517/#528/#527), unbuilt here on purpose.
- Cross-till propagation of status changes over LAN sync — the local
  conflict rule is in place and exercised; wiring status into the sync
  protocol belongs with #516+.
- A one-paragraph ADR-0011 amendment recording the new "order status —
  forward-only monotonic ladder" conflict-rule category, for
  completeness (the rule itself is already fully documented in code).
- The three non-blocking findings above.
