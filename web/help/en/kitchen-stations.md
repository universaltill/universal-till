---
id: kitchen-stations
title: Kitchen stations
section: Running the business
order: 235
summary: Route each food category — or a single item — to its own kitchen printer or kitchen screen, so the grill order prints at the grill and the drinks order prints at the bar.
keywords: [kitchen, station, printer, display, screen, routing, ticket, grill, bar]
routes: [/kitchen-stations, /kitchen-display/{station_id}]
---

# Kitchen stations

Route each food category — or a single item — to its own kitchen printer or kitchen screen, so the grill order prints at the grill and the drinks order prints at the bar.

## How to use it

1. Open **Kitchen stations** from the menu (manager only) and create a station for each place food is prepared — for example "Grill" or "Bar". Choose its **Destination**: **Printer** (a ticket), **Display** (a kitchen screen — see below), or **Printer and display**. A station that prints needs its printer's network address or device path; a display-only station doesn't. If the printer is already on this network, click **Find printers on this network** first — it lists any it finds so you can pick one instead of typing the address by hand; nothing is added until you also save the station.
2. In **Category routing**, tick the stations each category's items should print at. This is the main way to route — one tick covers every item in the category.
3. For the odd item that should go somewhere different, search for it under **Item overrides** and tick its stations. An item override replaces the category rule for that item only.
4. When a sale completes, each printing station prints its own ticket with just its lines. An item routed to two stations prints on both. Anything not routed anywhere — including an item whose only station is display-only — prints on the default kitchen printer from Settings, exactly as before.

## Kitchen display (a screen instead of, or as well as, a ticket)

A station whose destination is **Display** or **Printer and display** has its own live order screen. Click **View display** next to the station to open it, then move that window onto the second monitor plugged into this till — it's a page on this till, so nothing needs pairing or a network.

- The screen lists the orders that have at least one item routed to this station, newest first, with the same one-tap **Preparing** / **Ready** / **Collected** buttons as the Orders page. It refreshes itself every few seconds and the moment any order's status changes.
- Status belongs to the whole order, not to each item: an order with items for two stations shows on both screens, and marking it Ready or Collected on either screen updates both.
- The screen only shows orders taken on **this** till — unlike the till's own Orders page, it does not show orders taken on another till, even once they've synced. On a shop with several linked tills, open the kitchen display on the till that actually takes the relevant orders.
- A deactivated station's screen stops working until you reactivate it; a printer-only station has no screen.

## Good to know

- Deactivate a station instead of deleting it — its items fall back to the default kitchen printer until you reactivate it.
- One printer being unreachable never blocks the other stations or the sale itself.
