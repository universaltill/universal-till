# Review — kiosk checkout demands the charges its cart quotes (ut-docs#2937)

- **Branch:** `fix/2937-kiosk-checkout-charges`
- **Card:** universaltill/ut-docs#2937 (complexity:medium)
- **Author:** Opus 5.5 (lane:cloud-24) · **Reviewer:** Fable (independent, different model)

## What shipped

The self-order kiosk basket (`KioskEngine`) previews the merchant service
charge and every ADR-0062 plugin levy via `pos.BuildCharges`, but
`kioskSaleLinesAndTotal` (`internal/pages/self_order_shop.go`) built a
lines-only total with no `SaleInput.Charges`, so a customer was quoted more
than they paid and no charge was recorded. The card's own example: a 10%
service charge, one 5% levy and a 10.00 @20% exclusive basket quoted 13.80
and charged 12.00.

Decision (card option **b**): the kiosk checkout demands and persists
the same charges it quotes. It builds them with `pos.BuildCharges` from the
same runtime state as the cashier tender (`pos_api.go`), and
`eng.ChargePolicy()`. It passes them as `SaleInput.Charges` and adds them
(plus their `ChargesTax` on exclusive pricing) to the demanded total. A
statutory levy can't be skipped on a kiosk sale, and Toast lets merchants
apply service charges to kiosk orders
(https://support.toasttab.com/en/article/Setting-Up-Your-Kiosk). The
function's own invariant was already "a kiosk checkout must land on the same
total a cashier would for an identical basket". A Turkish till (ut-docs#962
ban) still gets no charge line.

Counter-mode and table-QR checkouts create no sale; the cashier rings them
up later on the cashier engine, which already applies charges. They were
already consistent, so they are unchanged.

The kiosk cart now also lists a **Service Charge** line when the charge is
positive, because an unattended customer must see what they pay. It reuses
the existing `journal.detail.service_charge` key, so no new locale key or
language-pack follow-up is needed (same reuse precedent as ut-docs#582).
Per-levy lines stay with ut-docs#986. `web/help/*/self-order.md` (en, de,
ar, fa, tr) gains one sentence.

## Findings (Fable review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Help said "the cart shows it", but the kiosk cart rendered only Subtotal/Tax/Total, so the customer would now pay an unlisted charge | **Fixed**: cart line added (+ test assertion; TR test asserts it's absent) |
| 2 | minor | Rate/forbidden came from `eng.Config()` while tax mode came from `d.CurrentState()` (mixed sources) | **Fixed**: all from runtime state, same as the cashier path and `SaleInput.TaxInclusive` |
| 3 | nit | de: "Servicezuschlag" vs the established "Servicegebühr"; pronoun agreement | **Fixed** |
| 4 | nit | fa ezafe spelling differs from `reports.md` | Accepted: both correct Persian |
| 5 | nit | Pre-existing: `lineNet` not floored at 0 | Accepted: no kiosk route sets a line discount; `computeSaleTotals` rejects it |

Also checked, with no problem found: rounding parity (the test pins cart
tax == persisted tax), fiscal-sign payload (built from `SaleInput.Charges`),
refunds (prorate persisted `sale_charges`), receipt, the TR ban, no `.Engine`
reference in the kiosk file, and no file writes (MkdirAll/paths.Data n/a).

## Verification

- TDD: `TestSelfOrderCheckout_ChargesMatchCartPreview` failed before the fix
  with `total drift: kiosk cart 1380, checkout demanded 1200, persisted 1200`
  (inclusive: 1150 vs 1000). The reviewer re-verified this independently in
  a separate worktree. The cart-line assertion was separately confirmed to
  fail with the template line reverted.
- `TestSelfOrderCheckout_ForbiddenChargesStayOff` is a regression guard (TR:
  total 1200, no charge, no cart line).
- Gate: `gofmt`, `go build ./...`, `golangci-lint` (0 issues), `go test ./...`
  (all ok), and every `ci.yml` build-job guard. Two guards could not run
  here: `guard-deadcode-baseline` needs GTK headers (it flags
  `internal/logging` funcs used only by `cmd/unitill-desktop`, unrelated),
  and `guard-shellcheck-version` needs a shellcheck binary. The docs-shots
  surface hash was refreshed without regenerating: no docs shot covers the
  self-order screen, and no seed sets a service charge, so the new line
  can't appear in any shot.
- **Driven run:** a real till (seed_demo, 10% service charge,
  tax-inclusive) at /self-order/shop in Playwright Chromium. The cart
  showed `Subtotal £7.20 / Service Charge £0.72 / Tax £1.32 / Total £7.92`,
  and the payment picker showed the same total. A real card checkout
  persisted `total 792`, payment `792`, and one `sale_charges` row
  `service_charge 72`, cashier `kiosk`. Looked at: en 1280×800, fa
  1280×800 (RTL, Persian digits, line aligned with its neighbours), de
  768×1024 (no German pack in the bare run, so English labels; layout
  fine). Not looked at: dark theme, a 7" kiosk.

## Verdict

Safe to merge.

## Deferred

- Per-levy lines in the kiosk cart / receipt: ut-docs#986 (existing card).
