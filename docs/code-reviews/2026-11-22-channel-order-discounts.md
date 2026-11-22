# Review: online order page shows line discounts (ut-docs#4945)

Dev record; Reviewer findings to be appended.

- `ChannelOrder.LineDiscountTotal` and `ItemsSubtotal` (money.Money, pure Go).
- `channel_order_detail.html`: "Line discount" column; totals gain "Items"
  (first) and "Line discounts", shown only when a line or order discount exists.
- en.json + en.meta.json: 4 keys. English only. No CSS change.
- Help topics `channel-orders` and `reports` updated; `make docs-shots` run,
  manifest regenerated (3 hashes), PNGs unchanged.
- Tests: `TestChannelOrder_LineDiscountAndItemsTotals`,
  `TestChannelOrderDetail_{ShowsLineDiscounts,OrderDiscountOnly,NoDiscounts}`.
- Open: `guard-deadcode-baseline.sh` cannot run here (toolchain go1.26 vs go1.27 packages).
