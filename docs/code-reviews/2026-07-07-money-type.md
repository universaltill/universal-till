# Code Review — introduce money.Money type (compile-time minor-units enforcement)

Date: 2026-07-07
Scope: new `internal/money`; converted `internal/pos` (money.go, tax.go,
service.go, sales.go), boundaries `internal/ui/buttons.go`,
`internal/pages/pos_api.go`, `internal/pages/inventory_api.go`, and the template
money helper `internal/httpx/httpx.go`.

## Intent
Make the "money is always integer minor units" rule enforceable by the compiler
rather than documentation only. A distinct `type Money int64` means Money+Money
works while Money*rate / Money+int is a compile error — money can no longer be
silently mixed with quantities or basis-point rates.

## Design
- **`internal/money`** is a leaf package (no internal deps), so both `pos` (which
  imports `data`) and `data` can use it without an import cycle.
- `Money` is a named `int64`: **JSON marshals as the same integer** (wire format
  unchanged) and it round-trips `database/sql` (reflection fallback; explicit
  `Scan`/`Value` also provided).
- Helpers mirror the **existing** arithmetic exactly: `MulQty` == the old
  `AmountForQuantity` (`math.Round`); `MulDiv` == the old half-up tax rounding.
  Where the original used different rounding (percent discount `(sub*bp+9999)/10000`,
  the return-total truncation in inventory_api), the exact expression is preserved
  via `.Minor()` / `FromMinor` — **no financial behavior change**.

## Boundary handling
- `pos` holds Money end-to-end; it converts to `int64` with `.Minor()` only at the
  `data` repository calls (InsertSale/SaleLine/Payment/Discount). `data` is
  untouched.
- Request DTOs in `pages` (camelCase, external contract) stay `int64` and are
  wrapped with `money.FromMinor` at the `pos` boundary; receipt display DTOs stay
  `int64` (`.Minor()` at the seam).
- The template `money` helper now accepts `any` (Money | int64 | int) so templates
  render both typed basket amounts and int64 display DTOs.

## Runtime issue caught & fixed
`TestScanHandlerUpdatesBasketTotals` failed after the field type change: the
template func `money(int64)` could not receive `money.Money`, so the totals line
failed to render. Fixed by making the helper polymorphic (and updated its unit
test). This is exactly the kind of silent template break the tests guard.

## Verification (performed)
- `go build ./...` clean; `gofmt` clean; data-access guard passes.
- **Full `go test ./...` green** — including pos pricing/tax/sales, pages
  scan/receipt/journal/promo, httpx, and the new `internal/money` unit tests
  (arithmetic, rounding parity, JSON-is-numeric, Scan/Value).

## Findings
- `Basket.DiscountRaw` deliberately kept `int64` (it holds either minor units or
  basis points depending on discount type — not purely money).
- Remaining money still typed as `int64`: the `data` repository layer and some
  `pages`/receipt display DTOs. These are boundaries; extending Money into them is
  a safe follow-up, not required for the enforcement win in the pricing engine.

## Disposition
Approved. Pricing/tender/sale engine is now money-type-safe end to end with no
behavior change; one latent template bug found and fixed.
