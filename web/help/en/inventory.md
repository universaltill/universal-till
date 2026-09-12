---
id: inventory
title: Stock & inventory
section: Setting up your shop
order: 120
summary: Tracks on-hand quantities per item and variant, and what a sale, refund or delivery does to them.
routes: [/inventory, /locations, /ui/inventory/stock-table]
# /locations is never screenshotted (docs-shots only captures routes[0]) —
# accepted gap, see e2e/tests-docs/lib.js's routedTopics() comment (ut-docs#900).
keywords: [stock, goods, receipt, delivery, adjustment, override, return, locations, transfer, low stock, reorder, count]
---

# Stock & inventory

Tracks on-hand quantities per item and variant, at each of your stock locations. Sales reduce stock automatically; goods-in, adjustments and refunds record deliveries, corrections and returns; a low-stock alert flags anything running out.

## How to use it

1. Open Inventory to see current stock levels — one row per item (or variant) and stock location.
2. Tap a stock row to open the receive/adjust popup prefilled with that row's item and location, or use the **+** button beside the search box for a blank one you fill in yourself. Record a delivery with goods-in; use an adjustment for waste, breakage or count corrections — enter a negative quantity to remove stock (on a touch till, tap the on-screen keyboard's "-" key first). Check the **Location** field before saving: it always targets the location shown, not "wherever this item normally sits" — saving against the wrong location creates a brand-new stock row for that item there instead of changing the row you meant, and the two then sit side by side in the table under the same item name.
3. The inventory page predicts how many days of stock remain and suggests how much to order, from that row's own last 28 days of sales — an item's row from its own direct sales, a variant's row from that variant's own sales, never each other's — see **Low-stock alerts** below for exactly what this needs to work. The reports page carries the same alert as a chip too.
4. Stock locations are shop-wide and always managed from the **main till** — see **Stock locations** below for creating, activating/deactivating and moving stock between them.
5. Tap the filter icon beside the search box to open the category list, then tap a category to narrow the list to items in that category — tapping a category that has sub-categories includes their items too. It combines with the search box; tap **All categories** to clear it.

## Manager override — negative stock

The **Manager override — negative stock** panel is a paper trail, not a stock adjustment. Filling in an item, its location, the quantity you found, a reason, and — for a cashier — a manager PIN writes an entry to the audit log recording that a manager approved letting this item run negative here, and why; a manager or admin can authorize their own without a PIN. It does **not** itself change the item's stock number, and it does not unblock a sale that's currently being refused for insufficient stock. To actually change how much stock is on hand, use the receive/adjust popup above instead — an adjustment there needs no manager PIN and is never blocked by low or negative stock. Use this panel afterwards, or alongside it, to put an authorized reason on record.

## Processing a return from here

The **Process a return** panel puts stock back and records a cash return against an original receipt, without going through the full Refund screen. Type the original receipt number and, once the till finds it, pick which lines and how many of each you're taking back — each line shows how many are still returnable, which drops once part of it has already been returned or refunded (through here or through Refund) so you can never take back more than was actually sold. Add a reason and press **Process Return**.

## How a sale and a refund change stock

Every sale, self-order or kiosk purchase draws stock from the location assigned to the **register** it's rung up on. Each till has its own register (chosen once in **Settings → Tills**), and a manager can point that register at one of your stock locations in **Settings → Registers** — a kiosk in the back unit at "Warehouse", say, while the counter till stays on "Main". A register with no location assigned draws from your shop's **Main** location, which is what every register starts as, so a shop that never touches the Registers page keeps working exactly as before. There's no per-sale choice of location: a till always sells from its register's location. Completing a sale reduces that location's stock row for each line's item by the quantity sold. If that would take an item below zero there and neither **Settings → Stock → "Sell items without tracking stock"** nor that item's own **Not stock-tracked** flag is switched on, the sale is refused outright with "Not enough stock to complete this sale" — the basket is left exactly as it was so you can adjust it (remove or reduce the line, or receive more stock first) and try again. This is also why receiving a delivery into a location that no register is assigned to can be misleading: no sale ever draws it down, so watch the location your registers actually sell from if it's the one running low. If you retire (deactivate) a location a register was assigned to, that register goes back to selling from Main until you assign it another.

A refund taken from the **Refund** screen (Journal → sale history) adds the returned quantity straight back to the stock row for that item at the refunding till's register location (Main when none is assigned), the moment the refund completes — see **Selling & checkout** for the refund steps themselves. Restocking a refund is always allowed, even for an item that's since gone negative. Opening stock from a catalogue import lands at the importing till's register location the same way, and a sale rung up on a joined till and synced to the main till is applied there against the location of the register that sold it.

## Low-stock alerts

Two separate signals warn you about running low, and only one needs any setup:

- **Days left**, in the stock table's own column, and the ⚠ chip on the Stock Levels card here and on the Reports page's own header — works out of the box, purely from that row's own last 28 days of sales against how much is left: a fast seller with little stock warns; a row with no sales history of its own simply shows "—", never a guess. Setting an item's **Lead time (days)** on its Catalog **Variants** tab sharpens both the warning window and the suggested order quantity to how long that item actually takes to restock, instead of a flat default.
- **Reorder at**, in the stock table, and the **Low Stock** list further down this page — both are driven by a reorder level set per item, which this build has no screen to set from the till yet: on a fresh shop neither one has anything to show ("—" in Reorder at, "No low stock items" in the list) until that's added. Days left above works today regardless.
- **Items sold in variants** (size, colour, …) get their own row per variant on both signals, listed separately from the item's own row if it separately carries stock at the item level too (not necessarily next to it) — a variant's stock, and its own sales rate driving Days left, is never folded into or hidden behind its parent item's row (or the other way around), so each variant's own low-stock/reorder state is always visible on its own, from its own sales alone. A variant's row is read-only here: tapping it does not open the receive/adjust dialog, since that dialog does not yet have a way to pick a specific variant.

## Stock locations

Locations (manager only) are the separate physical places you hold stock — a shop floor, a back room, a second unit's warehouse. Every stock number in Inventory belongs to exactly one location, so the same item can show more than one row: its own quantity at each place you stock it.

1. Open **Locations** from the menu (manager only) to see every location and whether it's active.
2. To add one, type its name under **New location** and press **Create location** — it appears in the list immediately, active by default. A name that's already used is refused.
3. To rename one, edit the name in its own row and press **Rename**.
4. To retire one, press **Deactivate** — it drops off the receive/adjust dialog's Location picker so nothing new gets recorded against it, but its history stays intact either way. It's refused only while the location still holds stock or has an active register assigned: clear its stock to zero (or move it to another location first) and unassign or retire its register, then deactivating goes through. A shop must always keep at least one active location; deactivating the last one is refused too, though with a different message than the in-use refusal above. **Activate** brings a retired location back, and it reappears in the receive/adjust picker immediately.
5. Creating, renaming, activating or deactivating a location only works from the **main till** — on a joined till, any of the four shows a message pointing you back to the main till instead of taking effect locally.

## Moving stock between locations

There's no separate "transfer" button — move stock by recording two ordinary movements against the same item from the receive/adjust popup on this page: an **adjustment** with a negative quantity at the location you're moving stock out of, and a **stock receipt** (or another adjustment) with a positive quantity, same item, at the location you're moving it into. Recording only one side leaves the two locations' totals out of step with what's physically happened, so do both before moving on.

## If you do not track stock at all

Some shops never count stock — they just want every item to sell, every time. Turn on **Settings → Stock → "Sell items without tracking stock"**. Items then sell even when the till has no stock record for them, and no sale is ever refused for being out of stock.

Leave it off if you want the till to stop a sale when an item runs out. That is the default, and it is why a catalogue imported from a system that did not track stock cannot sell anything until you turn this on.

When you import from a system that records **"Track inventory? No"** for an item, the till does not treat that item's quantity column as a real stock level — the import tells you it did not carry it, rather than inventing an on-hand figure your old system never claimed.

## Stop tracking stock for one item

The setting above is shop-wide. If only a few items should never carry stock — a delivery-only line, a keg you don't count individually — tick **Not stock-tracked** on that item's own entry in the Catalog, instead of turning the shop-wide setting on for everything. That item then sells freely, is skipped by every stock check, and never shows up in Inventory or on a low-stock list, while every other item in your shop keeps being tracked normally.
