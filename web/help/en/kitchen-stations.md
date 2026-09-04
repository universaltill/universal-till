---
id: kitchen-stations
title: Kitchen stations
section: Running the business
order: 235
summary: Route each food category — or a single item — to its own kitchen printer, so the grill order prints at the grill and the drinks order prints at the bar.
keywords: [kitchen, station, printer, routing, ticket, grill, bar]
routes: [/kitchen-stations]
---

# Kitchen stations

Route each food category — or a single item — to its own kitchen printer, so the grill order prints at the grill and the drinks order prints at the bar.

## How to use it

1. Open **Kitchen stations** from the menu (manager only) and create a station for each place food is prepared — for example "Grill" or "Bar" — with its printer's network address or device path. If the printer is already on this network, click **Find printers on this network** first — it lists any it finds so you can pick one instead of typing the address by hand; nothing is added until you also save the station.
2. In **Category routing**, tick the stations each category's items should print at. This is the main way to route — one tick covers every item in the category.
3. For the odd item that should go somewhere different, search for it under **Item overrides** and tick its stations. An item override replaces the category rule for that item only.
4. When a sale completes, each station prints its own ticket with just its lines. An item routed to two stations prints on both. Anything not routed anywhere prints on the default kitchen printer from Settings, exactly as before.

## Good to know

- Deactivate a station instead of deleting it — its items fall back to the default kitchen printer until you reactivate it.
- One printer being unreachable never blocks the other stations or the sale itself.
