# 2026-07-28 — Order-type (dine-in/takeaway) VAT switching

## Context
`docs/germany-pos-parity-backlog.md` ("VAT rate must switch on dine-in vs.
takeaway") and ADR-0025 (accepted 2026-07-28): German law (§12 UStG)
requires some items to tax at a different rate for takeaway than dine-in
(19% eat-in / 7% takeaway for drinks), while others (cakes) stay pinned to
one rate regardless. Universal Till had an `OrderType` field
(`internal/print/kitchen.go`) used only for kitchen-ticket labels, and a
per-line `TaxRateBasisPoints` on sale lines, but nothing connected the two —
confirmed before starting, per the backlog doc's own scoping.

## Design
- **`tax_codes.takeaway_rate_basis_points`** (new nullable column, migration
  `020_order_type_tax_rate.sql` — numbered 020, not 019: `019` was already
  claimed by a parallel PR (`feat/tip-amount-domain-model`, tip_amount on
  `payments`) at the time this branch was cut. Whichever of the two merges
  second will need renumbering to avoid a collision — flagged for whoever
  merges.). NULL = no override, so every existing tax code's behaviour is
  unchanged. Set on the item's own tax code — e.g. a drinks code gets
  `rate_basis_points=1900, takeaway_rate_basis_points=700`; a cakes code
  gets `rate_basis_points=700` and no override at all (already-existing
  mechanism — nothing new needed for "always reduced" items).
- **`pos.Config.ReducedTaxRateBasisPoints`** (new): the shop's global
  takeaway rate, for items with **no** per-item tax code at all (same
  "0 = unset" convention `TaxRateBasisPoints` already uses). Threaded
  through `common.RuntimeState.ReducedTaxRatePct` → settings key
  `store.reduced_tax_rate` → `setupCountries` (only `DE` populated with a
  real, verified rate — 7%; every other country's reduced rate is
  deliberately left at 0 rather than guessed, since researching each
  country's actual reduced/zero VAT rate was out of scope for this change).
- **`pos.Service.SetOrderType`**: sets the sale's order type ("" or
  `pos.OrderTypeTakeaway`) and recomputes totals immediately — including
  lines added *before* the order type was chosen or changed, per the
  backlog's explicit requirement (a customer switching eat-in/takeaway
  mid-order). New `/api/pos/order-type` endpoint + a dine-in/takeaway
  toggle on the basket partial drive it.

**Judgment call, the one most likely to matter for review**:
`effectiveTaxRateBP` (`internal/pos/service.go`) treats "has its own tax
code" and "has no tax code at all" as genuinely different cases, not one
falling back to the other. A first pass conflated them — a line with an
explicit tax code but no takeaway override incorrectly fell back to the
*global* reduced rate instead of staying pinned to its own rate. Caught by
`TestOrderTypeTaxSwitching`'s three-line case (drink/cake/merch) failing
with the wrong total (170 instead of 190) before the fix; the pinned-cake
case is exactly the scenario the fallback-conflation bug broke silently.

**`recomputeTotals` was changed from one subtotal-wide `TaxEngine.Compute`
call to a per-line sum.** The subtotal-wide engine field (`Service.tax`)
was removed entirely — a single flat rate over the whole basket can't
represent per-line tax codes *or* an order-type switch that only affects
some lines. This also fixes a pre-existing (unrelated to order type)
inaccuracy: a basket mixing two different tax-coded items was always
under/over-stating its live preview tax before this change, since the flat
engine ignored per-line `TaxRateBP` entirely for the on-screen total (only
the final persisted sale, computed separately in `pos_api.go`'s tender
handler, was ever per-line-accurate). Tender-time tax resolution
(`pos_api.go`, `self_order_shop.go`) now calls the same
`Service.EffectiveLineTaxRateBP` the live preview uses — one source of
truth, so what a cashier sees pre-payment is what gets recorded/receipted
(`self_order_shop.go`'s `kioskSaleLinesAndTotal` has a pre-existing
docstring invariant requiring kiosk totals to match the cashier path
exactly, which this change would have silently violated if only the
cashier tender handler were updated).

## What changed
- `internal/db/migrations/020_order_type_tax_rate.sql` (new, append-only).
- `internal/data/pos_repo.go`: `takeaway_rate_basis_points` added to
  `shortcutPriceRow`/`ShortcutLine` and all five `resolve*` SQL queries.
- `internal/ui/buttons.go`: `PriceResolverAdapter` carries it to
  `BasketLine`.
- `internal/pos/service.go`: `BasketLine.TakeawayRateBP`,
  `Config.ReducedTaxRateBasisPoints`, `OrderTypeTakeaway` const,
  `effectiveTaxRateBP`, `Service.orderType`/`OrderType()`/`SetOrderType()`/
  `EffectiveLineTaxRateBP()`, `Basket.OrderType`; `recomputeTotals` rewired
  to per-line tax as above; `orderType` resets in `Reset()`/`Tender()`.
- `internal/pages/common/deps.go`, `state.go`: `RuntimeState.ReducedTaxRatePct`,
  `KeyReducedTaxRate`, load/save, generic-upsert `case`.
- `internal/pages/init.go`, `settings_page.go` (×2), `setup_page.go`:
  every `pos.Config{...}` construction site now also sets
  `ReducedTaxRateBasisPoints`.
- `internal/pages/pos_api.go`, `self_order_shop.go`: tender-time tax now
  calls `Service.EffectiveLineTaxRateBP` instead of each duplicating its
  own fallback logic.
- `internal/pages/setup_page.go`: `setupCountry.ReducedTaxRatePct` (DE=7,
  others=0); `web/ui/pages/setup.html` wizard prefills it from the country
  picker, same pattern as the existing tax field.
- `web/ui/partials/basket.html`: dine-in/takeaway toggle buttons.
- `web/locales/{en,ar,fa,tr}.json`: `basket.order_type.*` keys (ar/fa/tr
  are reasonable machine translations, not native-verified).
- Tests: `internal/pos/service_test.go` (`TestOrderTypeTaxSwitching` — the
  drink/cake/merch three-line case that caught the pinning bug above),
  `internal/ui/buttons_test.go` (`TestPriceResolverAdapter_TakeawayRateBP`
  — verifies the new column reaches `BasketLine` through real SQL, plus
  the existing `tax_codes` inline-schema fixtures in this file,
  `internal/testsupport/sqlite_catalog.go`, and
  `internal/pages/ui_smoke_test.go` updated with the new column, which two
  pre-existing tests needed to keep passing).

## Explicitly out of scope
- No tax-code management UI exists for **any** tax code today (confirmed:
  `web/ui/partials/catalog_lookups.html` is a read-only autocomplete
  datalist, no create/edit form anywhere) — so a merchant cannot yet set a
  cake's pinned rate or a drink's takeaway override without direct DB
  access. This is a pre-existing gap this change doesn't introduce or
  fix; a real follow-up, not silently assumed away.
- Only Germany's reduced rate is populated in `setupCountries`; other
  countries' reduced/zero VAT rates were not researched.
- Self-order kiosk mode gets order-type-aware totals for free (shares
  `Service`), but has no UI to actually set order type — likely moot for a
  self-order kiosk in practice, not addressed either way.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` all pass.
`scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` both pass.
