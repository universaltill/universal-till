# Code review — low-stock prediction: days of stock left (2026-07-17)

Branch `feat/low-stock-days-left`. First slice of the Phase-1 "predictions +
alerts" ask (Farshid: "item X will run out in ~N days at current rate").

## What changed
- `POSRepo.ItemDailySellRates(days)` — average units/day per item over the
  last N days (default 28) from completed sales, returns subtracted; raw SQL
  in the repo layer per the data-access rule.
- Inventory page: new **"Days left"** column = on-hand ÷ daily rate.
  ≤7 days → ⚠ amber warning on the row; header shows a count chip
  ("N item(s) predicted to run out within a week"). Items with no sales
  history show an em-dash (no bogus predictions); zero stock with an active
  rate shows 0 + warning.
- i18n ×4 (en/tr/fa/ar), logical-CSS styling, offline (pure SQLite).

## Scope notes
- Deliberately core (not a plugin): the wasm host exposes no sales-history
  functions, and stock display is already core UX. The bigger forecasting
  arc (seasonal order-ahead) stays queued.
- Variant-level rates fold into their parent item (sale_lines carries
  variant OR item; variant lines are excluded for now — follow-up:
  aggregate variant sales into the item id via the variants table).

## Tests
`TestInventoryPredictsDaysLeft` — real migrations, seeded 28-day sales
(≈2/day), asserts the repo rate, the rendered warning on the fast seller,
the header chip, and no prediction for the no-history item. Full pages+data
suites and both guards green.
