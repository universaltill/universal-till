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
2. Tap a stock row — or the **+** button beside the search box — to open the receive/adjust popup, prefilled with that item. Record a delivery with goods-in; use an adjustment for waste, breakage or count corrections — enter a negative quantity to remove stock (on a touch till, tap the on-screen keyboard's "-" key first).
3. The inventory page predicts how many days of stock remain and suggests how much to order; the reports page carries a low-stock alert chip too.
4. Stock locations (Locations, manager only) are shop-wide and always managed from the **main till**: on a joined till, creating, renaming or deactivating one shows a message pointing you back to the main till instead.

## Processing a return from here

The **Process a return** panel puts stock back and records a cash return against an original receipt, without going through the full Refund screen. Type the original receipt number and, once the till finds it, pick which lines and how many of each you're taking back — each line shows how many are still returnable, which drops once part of it has already been returned or refunded (through here or through Refund) so you can never take back more than was actually sold. Add a reason and press **Process Return**.

## If you do not track stock at all

Some shops never count stock — they just want every item to sell, every time. Turn on **Settings → Stock → "Sell items without tracking stock"**. Items then sell even when the till has no stock record for them, and no sale is ever refused for being out of stock.

Leave it off if you want the till to stop a sale when an item runs out. That is the default, and it is why a catalogue imported from a system that did not track stock cannot sell anything until you turn this on.

When you import from a system that records **"Track inventory? No"** for an item, the till does not treat that item's quantity column as a real stock level — the import tells you it did not carry it, rather than inventing an on-hand figure your old system never claimed.

## Stop tracking stock for one item

The setting above is shop-wide. If only a few items should never carry stock — a delivery-only line, a keg you don't count individually — tick **Not stock-tracked** on that item's own entry in the Catalog, instead of turning the shop-wide setting on for everything. That item then sells freely, is skipped by every stock check, and never shows up in Inventory or on a low-stock list, while every other item in your shop keeps being tracked normally.
