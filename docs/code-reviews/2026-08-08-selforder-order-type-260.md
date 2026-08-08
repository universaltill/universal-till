# Code review: self-order kiosk dine-in/takeaway order-type selector (ut-docs#260)

**Date:** 2026-08-08
**Card:** [ut-docs#260](https://github.com/universaltill/ut-docs/issues/260)
**Complexity:** medium
**Author (Dev):** scrum-master pipeline, inline (Sonnet)
**Reviewer:** independent Opus subagent, fresh context, isolated worktree

## What shipped

The self-order kiosk (`internal/pages/self_order_shop.go`) had no way for
an anonymous customer to say "takeaway" even though the checkout handler
already threaded `d.Engine.OrderType()` into `SaleInput.OrderType`, and
`pos.Service.SetOrderType`/`OrderTypeTakeaway` — the dine-in/takeaway
mechanism the cashier's basket already exposes via
`POST /api/pos/order-type` (ADR-0037/#37) — was fully generic and ready to
reuse. The gap was purely a missing kiosk-facing endpoint + UI, not new
tax logic.

- `internal/pages/self_order_shop.go`: new `POST /api/self-order/order-type`,
  a direct mirror of the cashier's handler in `pos_api.go` — same clamp
  (anything other than the exact `pos.OrderTypeTakeaway` sentinel,
  including `""`, means dine-in), calling the same `Service.SetOrderType`.
- `web/ui/partials/self_order_cart.html`: a dine-in/takeaway toggle at the
  top of the kiosk cart, mirroring `basket.html`'s existing markup/ARIA
  pattern, posting to the new endpoint. Buttons carry `.btn-touch` (kiosk
  is an anonymous walk-up touchscreen, unlike the cashier's mouse/keyboard
  basket).
- `web/locales/{en,ar,fa,tr}.json`: new `selforder.order_type.{label,dine_in,takeaway}`
  keys in all four locales, reusing the already-translated
  `basket.order_type.*` copy verbatim (own `selforder.*` namespace, so
  kiosk wording can diverge from the cashier's later if needed).
- `web/help/{en,ar,fa,tr}/self-order.md`: one sentence in each locale
  describing the toggle — worded to say what it actually does (records
  the choice on receipt/journal/kitchen ticket; only changes the charged
  tax if a tax plugin acts on it), not what a shop owner might assume.
- `internal/pages/self_order_shop_test.go`: two new tests,
  `TestSelfOrderShop_OrderTypeToggleSetsTakeaway` and
  `TestSelfOrderShop_CheckoutPersistsSelectedOrderType`, both TDD
  (confirmed failing pre-fix — 404 and empty `order_type` respectively —
  before the handler existed).
- `web/help/img/**` + `manifest.json`: regenerated via `make docs-shots`
  (the app-surface hash changed, so the guard requires a full recapture
  across all 15 routed topics × 4 locales; only `alerts`/`designer` came
  out pixel-different, and that's a live timestamp in the "Recent
  Problems" panel — a checked screenshot, not a genuine layout change).

## Independent review findings (Opus, isolated worktree)

The reviewer ran the full gate for real (build/vet/guards/`go test ./...
-race`, all green — see its report for pasted output) and independently
re-verified the TDD claim by reverting just the handler and confirming
both new tests fail with the exact errors above, then restoring and
confirming both pass. Verdict was **not safe to merge as-is**, with two
blockers and two real-but-minor findings, all fixed in this branch before
merge:

1. **Blocker — `guard-docs-shots.sh` red.** The diff touches
   `web/ui/partials/self_order_cart.html` and `self_order_shop.go`, both
   inside the guard's hashed surface. **Fixed**: ran `make docs-shots`
   (via `npx playwright test --config=playwright.docs.config.ts` against
   the environment's pre-installed Chromium — a temporary
   `launchOptions.executablePath` was added to `playwright.docs.config.ts`
   only for the local run and reverted immediately after, since the
   pinned `@playwright/test` version doesn't match this sandbox's
   preinstalled browser revision; nothing about that override is in the
   committed diff) and committed the regenerated screenshots + manifest.
2. **Blocker — the manual sentence over-promised tax behaviour.** The
   original wording ("...the right tax rate applies automatically") is
   false on a default install: `Service.effectiveTaxRateBP` is
   order-type-aware only through an optional plugin tax hook: with no
   `taxAsker` installed, core just uses each line's configured rate,
   order-type-independent (per `service.go`'s own comment). **Fixed**:
   reworded in all 4 locales to say the toggle records the choice for
   receipt/journal/kitchen ticket, and only changes the charged tax if a
   region's tax plugin uses it.
3. **Real-but-minor — the `aria-pressed="true"` test assertion was
   tautological.** The reviewer proved it empirically: inverting *both*
   buttons' `aria-pressed` logic in the template still passed the
   original assertion, because exactly one button always carries
   `aria-pressed="true"` regardless of which one. **Fixed**: added a
   `pressedState` test helper (regex-matched per-button, pairing each
   button's own `aria-pressed` value with its own `hx-vals` order-type
   marker) and asserted on *which* button is pressed, not just that some
   button is. Re-verified the fix actually catches the class of bug the
   reviewer found: reproduced the reviewer's inverted-logic mutation
   locally, confirmed the updated test now fails against it, then
   restored and confirmed it passes again.
4. **Real-but-minor — missing touch-target sizing.** `.selforder-order-type`
   had no CSS rule, and the toggle buttons got only desktop `.btn` sizing
   while every sibling kiosk control (`.selforder-qty-btn`,
   `.selforder-checkout`, tiles) is explicitly touch-sized. **Fixed**:
   added the repo's existing `.btn-touch` (46px min-height) class to both
   buttons — no new CSS needed.

Two findings were triaged as accepted, not fixed:

- **Nitpick, accepted** — the new locale keys duplicate `basket.order_type.*`
  byte-for-byte in all 4 locales. Deliberate (own `selforder.*` namespace
  so kiosk copy can diverge later), just hand-synced for now.
- **Note, pre-existing, out of scope** — `/api/self-order/*` is
  auth-exempt by prefix and `d.Engine` is a single process-level
  `pos.Service` with no mutex, so an unauthenticated LAN client can flip
  order type (or already, scan/remove lines) on whatever basket the till
  holds. This diff adds no new *class* of exposure — `/api/self-order/scan|line|remove`
  already had it, more damagingly — but it does put a tax-relevant field
  on that surface for the first time. Worth its own issue against the
  whole `/api/self-order/*` surface (auth/pairing + engine locking), not
  this card; **filed as a new Backlog card** rather than silently
  dropped.

## Verified beyond automated tests

Drove the kiosk in a real headless-Chromium session (not just asserting
on rendered HTML strings), against the seeded Task Runner demo catalog:

- **English, 1280×900**: scanned an item, screenshotted the cart with the
  toggle in its default "Dine in" state (checkmark + blue highlight on
  the correct button, £ tax total correct), then clicked "Takeaway" and
  re-screenshotted — highlight moved to the correct button, cart total
  recomputed. No overlap, no misalignment, checkout button and totals
  unaffected.
- **Farsi (`?lang=fa`), RTL, 1280×900**: full page + cart screenshot.
  Toggle mirrors correctly under RTL (still top of cart panel, buttons
  fully legible, no logical/physical-property leak), translated labels
  ("صرف در محل" / "بیرون‌بر") render right-aligned with no truncation or
  wrap. (State shown was residual "takeaway" from the prior English click
  against the same shared server-side `pos.Service` instance — expected
  for this single-till, single-active-basket architecture, not a bug.)
- **Not visually checked this round**: ar/tr locales specifically (same
  short reused translated strings, already proven at these lengths via
  the cashier basket toggle, so risk is low) and any dark-theme kiosk
  variant (no dark-theme toggle exists on this customer-facing screen, so
  n/a).
- Manually confirmed via `curl` that `/api/self-order/cart` renders the
  toggle even with an empty basket (it sits above the
  `{{ if .Basket.Lines }}` branch), and that the checkout SaleInput really
  persists `order_type = 'takeaway'` on the `sales` row end-to-end (also
  covered by `TestSelfOrderShop_CheckoutPersistsSelectedOrderType`).
- Killed the manually-started server and removed all scratch files/DBs
  after driving it (`/tmp/e2e-manual.db`, screenshots, script files) —
  nothing left running or committed from the manual check.

## Gate (run once, after all fixes)

`go build ./...`, `go vet ./...` — clean.
`go test ./... -race` — all 36 test-bearing packages pass, no races, no
panics (`internal/pages` and `internal/pos` both green, ~215s/~4s resp.).
`bash scripts/ci/guard-i18n.sh`, `guard-data-access.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh` — all four pass.

No real client/shop name introduced anywhere in the diff (existing
`Flat White`/`Task Runner Cafe` test fixtures only).

## Safe-to-merge verdict

**Yes**, after the four fixes above. Deferred: the pre-existing
`/api/self-order/*` auth/locking exposure (new Backlog card), and
ar/tr-specific visual screenshots (low risk, same proven-length strings).

---
_Generated by [Claude Code](https://claude.ai/code)_
