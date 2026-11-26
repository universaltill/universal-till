# Till value gates follow-ups (ut-docs#6858, items 1, 3, 4)

Follow-ups from `2026-11-21-cashier-value-gates-5416.md` (round 1 F2, F4;
round 2 F2/F3; round 3). Item 2 (bind the approval to the *to* value) was split
to ut-docs#7085 and is not part of this change. No bypass existed; the gates
and audit held. This makes them exact.

## Changes

- **Item 1.** `pos.Service.SetDiscountIf(from, to)`: a compare-and-set on the
  computed sale discount (what `SaleDiscount()` returns), `ErrLineChanged` on a
  mismatch. `/api/pos/discount` always applies through it; a lost CAS is the
  existing stale 409 (`LineChangedKey = "sale"`), nothing audited. Tender reads
  `SaleDiscount()` once, so the gate and the audit `from` agree.
- **Item 3.** A request without `HX-Request` from a session lacking the gated
  permission gets 403 JSON (`writeForbiddenJSON`, the discount envelope) before
  the PIN is looked at, on `/api/pos/reset` (priced basket), `/api/pos/line`
  reduction, raise, and reduce+raise (both permissions). Keys reused:
  `pos.error.discount_not_permitted`, `common.error.manager_or_admin_required`;
  no new key, one meta description widened.
- **Item 4.** `/api/pos/discount` sends `HX-Retarget: #basket` only on the
  approval retry (`override_pin` or `from_discount`), as `/api/pos/reset`.

## Follow-ups noted, not done

- Tender's third `SaleDiscount()` read (charged amount when `in.Discount == 0`)
  is a money-path change; left alone.
- `/api/pos/remove` has the same non-HTMX 200-prompt shape.
- Round 1 F5 (equal-value post flattens a percent promo) unchanged.

## Tests

`internal/pos/service_discount_if_test.go`;
`internal/pages/pos_value_gates_6858_test.go`. Cashier posts in the 5416, line
CAS and line reduce-gate tests moved to `postFormHtmx` (they model the till's
page). Five e2e `/api/pos/reset` setup posts gained `HX-Request: true`.
