# Code review — variant-level cost editing (2026-07-17)

Branch `feat/variant-cost`. Completes the margins loop: the variant grid
gains a **Cost** column (major-unit input, same utCurrency.toMinor JS as
price; blank = unset, only sent when filled). The POST handler ALREADY
accepted `costPrice` (minor) into VariantInput — this was UI-only plus:
- `VariantEditView.CostMinor` + COALESCE(cost_price,0) in VariantsForItem;
- grid CSS 7 columns (scroll min-width bumped);
- prefill script fills cost inputs from data-minor when > 0;
- `catalog.col.cost` ×4 locales.

MarginByItem already prefers variant cost over item cost, so filled variant
costs flow straight into the margins card. Suites + both guards green.
