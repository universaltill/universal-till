---
id: inventory
title: Stock & inventory
section: Setting up your shop
order: 120
summary: Tracks on-hand quantities per item and variant.
routes: [/inventory, /locations, /ui/inventory/stock-table]
# /locations is never screenshotted (docs-shots only captures routes[0]) —
# accepted gap, see e2e/tests-docs/lib.js's routedTopics() comment (ut-docs#900).
keywords: [stock, goods, receipt, locations, count]
---

# Stock & inventory

Tracks on-hand quantities per item and variant. Sales reduce stock automatically; goods-in and adjustments record deliveries and corrections.

## How to use it

1. Open Inventory to see current stock levels.
2. Record a delivery with goods-in; use an adjustment for waste, breakage or count corrections.
3. The inventory page predicts how many days of stock remain and suggests how much to order; the reports page carries a low-stock alert chip too.
