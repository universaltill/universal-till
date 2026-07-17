# Code review — product reports: slow sellers + dead stock (2026-07-17)

Branch `feat/product-reports`. First increment of the "more owner reports"
ask (Farshid: reports to help the shop owner). Best sellers already existed
(TopItems); this adds the two decisions owners actually make from a report:
what to STOP stocking and what's sitting as dead capital.

## What changed
- `POSRepo.SlowItems(days, limit)` — TopItems ascending: the worst sellers
  that still had ≥1 sale in the window (returns excluded via sale_type).
- `POSRepo.DeadStock(days, limit)` — active items with on-hand stock and
  ZERO sales in the window; value = qty × base_price (minor units), most
  tied-up money first.
- Reports page: two new cards ("Slow sellers", "Dead stock" with an
  explanation line), driven by the existing period selector. i18n ×4.

## Tests
`TestSlowItemsAndDeadStock` on real migrations — notably the test db seeds
the DEMO CATALOG, so assertions target our seeded rows (sold item is slow
not dead; never-sold-with-stock is dead with correct value; zero-stock
never-seller is neither). Full pages+data suites + both guards green.

## Follow-ups (queued)
Margins per item/category (needs cost_price capture), year-over-year,
weekday/hour patterns, tax summaries.
