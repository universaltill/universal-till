# Code review — variant sales count toward parent items (2026-07-17)

Branch `fix/variant-rates`. Correctness fix for today's prediction/report
queries: sale_lines carry item_id OR variant_id (CHECK constraint), and both
ItemDailySellRates and DeadStock only looked at item_id — so an item sold
mostly via variants had an understated sell rate (days-left too optimistic)
and could appear as DEAD STOCK while selling briskly.

Both queries now fold variant lines into the parent item via
`COALESCE(NULLIF(sl.item_id,''), v.item_id)` (LEFT JOIN item_variants).
Margins already joined variants. Test extended: a variant sale removes the
parent from dead stock and gives it a rate. Suites + guard green.
