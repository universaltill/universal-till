# 2026-07-25 — Kiosk checkout (ADR-0020 Phase 4)

## Context
The self-order kiosk gets a real, money-moving checkout: pick a
card/contactless payment method (never cash — no drawer at a kiosk) and
complete the sale, unattended, with no cashier present. This is the
highest-stakes change in the whole ADR-0020 arc — every prior phase
(modifiers, kiosk shell, browse/cart) was reversible UI state; this one
calls `pos.CompleteSale` from an anonymous surface.

## Design
**The central decision**: extract the money-critical middle of the
existing cashier `/api/pos/tender` handler (`internal/pages/pos_api.go`)
— payment-authorization plugin gate → `pos.CompleteSale` → basket reset →
plugin `trigger_event` fan-out → ERP/inventory event mirroring → silent
receipt/kitchen printing — into a new shared function, `completeTender`,
called by *both* the (refactored) cashier handler and the new kiosk
handler. The alternative (writing a second, parallel money pipeline for
the kiosk) was rejected outright: two copies of "how a sale actually
completes" drifting apart is exactly the failure mode this session has
been avoiding since Phase 1 (`resolveAndValidateModifiers`) and Phase 3
(the scan/line handlers deliberately did NOT reuse cashier-only logic,
but validation logic that had no cashier-only baggage WAS shared).

`completeTender` deliberately excludes two things that stay in the
cashier's own handler: `SimFail` (a cashier testing hook with no kiosk
equivalent) and receipt/journal HTML rendering (the kiosk needs its own
confirmation screen, not the cashier's in-page receipt view).

**A pre-existing, previously-unused helper was found and adopted**:
`blockingPaymentEvent` in `internal/pages/refund_page.go` already existed
with a doc comment claiming it was "shared by the tender authorize gate
and the refund gate" — but grep showed the tender handler had never
actually called it; it duplicated the same logic inline instead. The
`completeTender` extraction now genuinely unifies the authorize gate
across tender and refund, making that comment true instead of aspirational.

**Payment-method validation is server-authoritative, with no fallback**:
`POSRepo.ListActiveNonCashPaymentMethods` (new) excludes `type='cash'` in
SQL. The kiosk checkout handler accepts only a `method` string present in
that server-fetched list — critically, unlike the cashier tender
handler's quick-tender-button fallback (`repo.EnsurePaymentMethod`, which
silently inserts a new `type='cash'` row for any unrecognized method id),
the kiosk path has no such fallback. An anonymous visitor cannot fabricate
a payment method or force cash.

**Totals are computed server-side from the live basket**
(`kioskSaleLinesAndTotal`), never from a client-submitted amount.
`CashierID`/`ActorID` are hardcoded to the literal `"kiosk"` string (the
seeded PIN-less operator from migration `018_kiosk_user.sql`) — never
taken from the request, unlike the cashier handler's signed-in-operator
`CashierID`.

## Independent review
Opus-model review, adversarial brief, weighted toward money correctness
(does the kiosk ever land on a different total than a cashier would for
the identical basket?), the `completeTender` extraction's fidelity, and
payment-method-validation bypass.

**Confirmed correct (reviewer verified independently, traced to source):**
- `kioskSaleLinesAndTotal`'s per-line subtotal/tax math is byte-for-byte
  the same formula as the old inline cashier computation and the
  authoritative `computeSaleTotals` in `internal/pos/sales.go` — no
  discrepancy in tax-inclusive handling, line discounts, or rounding.
  Modifier price deltas (folded into `PriceCents` at add-time, ADR-0020)
  are included correctly. The only deliberate difference is that kiosk
  sales carry no sale-level discount (no UI surfaces one to an anonymous
  customer) — documented in the code.
- `completeTender`'s order of operations and error-mapping are faithful
  to the pre-refactor inline code: a plugin decline still maps to 402
  with the identical `"payment declined: <method>"` message; insufficient
  stock still gets the cashier's in-page toast treatment (unchanged,
  stays in the caller); all other `CompleteSale` errors still map to 400.
- No bypass found for the cash/unknown-method rejection: `cash` and any
  string not in `ListActiveNonCashPaymentMethods`'s result are rejected
  before `pos.CompleteSale` is ever called, and nothing in
  `completeTender`/`blockingPaymentEvent`/`CompleteSale` re-introduces
  cash or fabricates a `payment_methods` row.
- Nothing reachable from `/api/self-order/checkout` lets an anonymous
  visitor influence `CashierID`, `ActorID`, `AllowNegativeInventory`, or
  the charged amount beyond their own kiosk session's basket contents.
- The `blockingPaymentEvent` payload (method/amount/reference, with
  `plugin_id` injected inside the helper) reproduces the old inline
  auth-gate payload exactly.
- The plugin-decline test (`TestSelfOrderShop_CheckoutDeclinedByPluginGateNeverCompletesSale`)
  is genuine, not tautological: it seeds a real `plugin_catalog` /
  `plugins` / `plugin_entries` / `plugin_hooks` chain and a real blocking
  subscriber that always errors — if the gate were ever bypassed,
  `CompleteSale` would succeed (stock is seeded) and the test would fail.

**One real finding, fixed before merge**: htmx (vendored 1.9.12) does not
swap a response body on a non-2xx status by default — it fires
`htmx:responseError` and silently discards the body instead. Every
`/api/self-order/*` 4xx handler (including Phase 3's pre-existing
`scan-with-modifiers` 400 path) returns a fully-rendered HTML re-render
with an inline i18n'd error message specifically so the customer sees
*why* — but with no page-level override, htmx would drop that body on
the floor. At a cashier till a swallowed error toast is a minor
annoyance; at an unattended kiosk with no cashier present it strands the
customer at a dead button with zero feedback — exactly the worst place
for this bug to live. Fixed with a page-scoped `htmx:beforeSwap` listener
in `self_order_shop.html` that forces `shouldSwap=true`/`isError=false`
for any 4xx response from `/api/self-order/*` (verified against the
vendored htmx 1.9.12 source directly — `requestConfig.path`, `xhr.status`,
and the `shouldSwap`/`isError` mutation contract all confirmed by reading
the minified bundle, not assumed from the htmx docs). This also
retroactively fixes the same latent gap in Phase 3's modifier-picker
error path.

**Two low-severity, no-fix-required observations:**
- The payment-picker's displayed total (`d.Engine.Basket().Total`, which
  aggregates tax across the whole basket and subtracts any sale
  discount) can theoretically diverge by a minor unit or two from the
  actually-charged total (`kioskSaleLinesAndTotal`, which sums per-line
  tax and never applies a sale discount) under mixed tax rates or if the
  shared engine ever carried a discount. For the kiosk's actual scope —
  single tax rate, no discount UI — these coincide exactly (the 384 = 320
  + 20% happy-path test assertion is correct). Not fixed: would require
  either unifying the two total computations or accepting the very
  narrow theoretical gap; flagged in `ut-docs/QUEUE.md` as a watch-item
  rather than blocking this PR.
- A zero-total basket (e.g. every line free) hits the
  `selforder.checkout.invalid_method` error key, which is misleadingly
  worded for that case. Cosmetic; not fixed.

**Pre-existing, out of scope**: `d.Engine` is one unsynchronized global
`*pos.Service` per till process (no mutex on the basket) — a property of
every phase since Phase 1, not introduced here. The kiosk's anonymous
reachability makes concurrent access somewhat more likely in theory, but
fixing shared-basket concurrency is a till-wide architectural change, not
a Phase 4 checkout concern.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, zero
regressions — every pre-existing tender/refund/blocking-payment-gate test
passes unmodified after the `completeTender` extraction, which is the
direct evidence the refactor is behavior-preserving for the cashier
path), `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
— all green, both by me and independently by the reviewer.

Live-verified against a real built binary and its own auto-loaded real
payment plugin (`com.universaltill.payment-demo`, hooked to
`payment.demo.authorize`) end-to-end over HTTP: opened the payment
picker (confirmed `cash` never listed, only `card`/`gift`/`qrpay`/
`demo_card`), completed a real sale through the live plugin gate,
confirmed the sale row (`cashier_id='kiosk'`, correct tax-inclusive
total) and basket reset, then confirmed `cash` and an arbitrary unknown
method both reject with 400 and leave no sale/basket-mutation behind.

New tests: `TestSelfOrderShop_CheckoutHappyPath`,
`TestSelfOrderShop_CheckoutRejectsCashMethod`,
`TestSelfOrderShop_CheckoutRejectsUnknownMethod`,
`TestSelfOrderShop_CheckoutRejectsEmptyBasket`,
`TestSelfOrderShop_CheckoutDeclinedByPluginGateNeverCompletesSale`.
