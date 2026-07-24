# 2026-07-24 — Kiosk browse/search/customize/cart (ADR-0020 Phase 3)

## Context
The self-order kiosk shell (Phase 2) gets its real flow: browse the full
active catalog as touch-friendly tiles (category chips + a real search
box — the first free-text item search anywhere in the sale flow, admin
catalog editor aside), tap-to-customize for modifier-bearing items, and a
locked-down cart (qty stepper, remove, running total). Checkout stays a
disabled placeholder — Phase 4.

## Design
**The central decision**: NOT a thin re-registration of the cashier's
`/api/pos/*` handlers under the exempt `/api/self-order/` prefix. Those
carry cashier-only behavior that must never reach an anonymous kiosk
visitor — `/api/pos/scan` `HX-Redirect`s to `/refund/{code}` when the
scanned code matches an existing receipt number, and `/api/pos/line`
accepts a free-text `discount` field with no bound/authorization check
(fine for a cashier the shop trusts, a live bill-cutting vector for an
anonymous customer). New, narrower handlers instead: `POST
/api/self-order/scan` does item-resolve-and-add only; `POST
/api/self-order/line` has no `discount` parameter in its vocabulary at
all — a qty-delta-based stepper instead, avoiding template-side
arithmetic.

The one piece of logic that IS shared: `resolveAndValidateModifiers`,
extracted from the cashier's `scan-with-modifiers` handler
(`pos_modifiers_api.go`) into a function both surfaces call. It has no
cashier-only baggage — pure server-authoritative validation against
catalog data — so sharing it reduces duplication risk (the code/itemId
mismatch check found by an earlier review stays in exactly one place)
rather than increasing it.

Also fixes a gap flagged in the Phase 2 shell's own spec note ("revisit
once there's real cart state to discard on reset"): landing on
`/self-order` now clears any in-progress basket, so an abandoned order
never greets the next customer.

## Independent review
Opus-model review, adversarial brief, weighted toward the central claim
(no cashier-only behavior leakage) and the basket-reset timing question.

**Confirmed correct (reviewer verified independently, traced to source):**
- The kiosk scan handler calls `d.Engine.ScanQtyWithResult` directly,
  which the reviewer traced into `pos/service.go`'s `scanQty` — confirmed
  the refund-redirect, promo-code fallback, and customer-barcode lookup
  all live in the cashier's HTTP handler wrapper, not the engine, so
  bypassing that wrapper genuinely removes them. Grepped the entire new
  file and all four new templates for "discount" — zero matches outside
  comments.
- The `resolveAndValidateModifiers` extraction is behavior-preserving:
  the code/itemId mismatch check (an earlier review's fix) survived
  byte-for-byte; all 8 pre-existing cashier-side modifier tests still
  pass unmodified after the refactor.
- The basket-reset-on-landing risk (could an authenticated manager
  checking the kiosk remotely wipe a real customer's cart?) is a
  non-issue: `d.Engine` is one instance per till *process*, so a
  manager's session on a different till has a different engine entirely.
  The only collision is hitting the exact same physical till process —
  which is definitionally "this kiosk," which has no concept of multiple
  simultaneous customers to protect between in the first place.
- `items.sku` is DB-`UNIQUE`; a browse tile's barcode-then-SKU fallback
  code computation has no new collision risk beyond what the shared
  resolver already has on the cashier path (pre-existing, not introduced
  here).
- `ItemIDsWithModifiers` is called once for the whole item-ID slice, not
  per-tile.
- No `template.HTML`/`safeHTML` bypass in any of the three new templates;
  all new CSS uses logical properties, no hardcoded `left`/`right`.
- The two security-relevant negative tests
  (`TestSelfOrderShop_ScanNeverTriggersRefundRedirect`,
  `TestSelfOrderShop_LineEndpointIgnoresDiscountParam`) would genuinely
  fail if a future change delegated to the cashier handlers or added a
  discount field.

**No findings requiring a fix.** Two minor, non-blocking observations:
items with neither a barcode nor a SKU are silently excluded from the
kiosk browse grid with no operator-visible warning (a UX nicety, not
correctness); the landing page's literal `←` back-arrow glyph doesn't
mirror for RTL locales (purely decorative, `dir="rtl"` still lays the
rest of the page out correctly).

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, zero
regressions), `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh` — all green, both by me and independently
by the reviewer. Live-verified against a real built binary and the app's
own pre-seeded demo catalog: browse (100+ items), search filtering,
adding a plain item, opening the modifier picker and adding a customized
item (correct folded price + sub-line), qty stepper up/down, remove
(confirmed it removes only the targeted line), and landing on
`/self-order` clearing an abandoned cart. New tests:
`TestSelfOrderShop_BrowseGridShowsActiveItems`,
`TestSelfOrderShop_SearchFiltersbyName`,
`TestSelfOrderShop_ScanAddsPlainItem`,
`TestSelfOrderShop_ScanNeverTriggersRefundRedirect`,
`TestSelfOrderShop_ModifierFlow`,
`TestSelfOrderShop_QtyStepperAndRemove`,
`TestSelfOrderShop_LineEndpointIgnoresDiscountParam`,
`TestSelfOrder_LandingResetsBasket`,
`TestSelfOrderShop_RoutesAreAuthExempt`.
