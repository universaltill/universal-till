# Code review — low-stock chip on reports (2026-07-17)

Branch `feat/lowstock-chip`. Tiny alerts increment: the days-left model
(28-day sell rate, ≤7-day threshold — same math as the inventory page)
now surfaces as a clickable ⚠ chip on the reports header linking to
/inventory, so the owner sees "N items running out" without opening
inventory. Reuses existing i18n keys. Suites + guards green.
