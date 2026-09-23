---
id: till-designer
title: Quick Buttons
section: Setting up your shop
order: 115
summary: Arrange the quick-sale buttons, product grid and categories shown on the sale screen, on a live copy of the sale screen itself.
routes: [/designer]
keywords: [designer, quick buttons, buttons, layout, product grid, quick sale, sale screen, categories]
---

# Quick Buttons

Arrange the quick-sale buttons, product grid and categories shown on the sale screen. The page shows the sale screen's own product panel — the same category strip and tiles cashiers see, live — and every change you make on it is saved straight away and shows on the sale screen immediately. Any item already in your [Catalog](/help/catalog) can become a quick button; Quick Buttons doesn't create items, it just picks which ones get a tile, what order they appear in, and which categories the strip is built from. Managers and admins only: a cashier doesn't see Quick Buttons in the menu at all, and the page itself refuses a cashier who types its address.

## How to use it

1. **Add a button:** type into the search box at the top — at least 3 characters (name, SKU or barcode); shorter than that shows a "Type 3+ characters" hint instead of searching. Matching items appear in a dropdown below the box; tap one and its tile appears in the sale-screen copy below straight away, no separate save step. If nothing matches, the dropdown says "No matches." — try a shorter or different search, or check the item exists and is active in the Catalog first.
2. **Reorder tiles:** press and hold any tile in the sale-screen copy (or right-click it) and the grid switches into the same edit mode the sale screen has: the tiles wobble, and you drag any tile to where you want it within its category, or move a focused tile one place at a time with the Left/Right arrow keys. Tap **Done**, press Escape, or tap anywhere outside the grid to save the new order. A tile can only be moved within its own category — putting an item in a different category is done in the catalog.
3. **Remove a button:** while the tiles are wobbling, tap the bin badge on a tile's corner and confirm. This only takes it off the sale screen — it never deletes the item itself from your Catalog, so you can always search for it and add it back later. The pencil badge on the other corner opens the item in the catalog and brings you back here afterwards.
4. **Manage categories:** the **Categories** list under the sale-screen copy shows every category — including ones with no quick buttons yet and ones you've deactivated, which the strip itself never shows. Tap **+ New category** to add one (name and an optional colour from the palette), the pencil on a row to rename or recolour it, and the up/down arrows to change the order the strip shows the categories in. Each row says how many quick buttons and how many catalog items the category has.
5. **Deactivate a category:** tap the bin on its row. It can only be deactivated while no active catalog item uses it — a row that still has items says so up front, with the count, and the till refuses the tap with the same message rather than silently doing nothing; move or deactivate those items in the Catalog first. A deactivated category leaves the strip until you tap **Activate** on its row; its items keep their category throughout.

## Good to know

- Quick-sale buttons and categories are shop-wide and always managed from the **main till**: on a joined till, adding, removing or reordering a button, or changing a category, shows a message pointing you back to the main till, rather than accepting a change that would only apply locally.
- The sale-screen copy shows a category only once one of its items has a quick button — exactly as the sale screen does — so a brand-new category won't appear in the strip until you add a button for one of its items. The Categories list below always shows it.
- Tiles on this page never sell anything: tapping one here doesn't add it to any basket. Everything else about them — colour, price, picture, order — is what cashiers see.
- No buttons set up yet? The grid shows "No products yet." — search and add your first one to get started.
- If the till refuses a reorder (for example, a server error), the grid reloads in the sale screen's real, saved order rather than leave a tile sitting in a position that never actually took. If your connection drops entirely instead, a message says so and the grid reloads the same way — repeat the move once you're back on the shop network.
- This is not the same page as the **Receipt designer** (Settings → Receipt printer → Receipt designer), which customises what prints on receipts — see [Receipt designer](/help/designer).
- Cashiers can rearrange tiles on the sale screen itself in the same way, without opening this page (see **Selling & checkout → Rearranging the quick buttons**) — dragging to reorder or tapping the bin badge asks for a manager's PIN on the spot before it actually saves, the same [manager-approval prompt](/help/elevation) used elsewhere. Categories can only be managed here (or under Items → Categories).
