---
id: till-designer
title: Quick Buttons
section: Setting up your shop
order: 115
summary: Arrange the quick-sale buttons and product grid shown on the sale screen.
routes: [/designer]
keywords: [designer, quick buttons, buttons, layout, product grid, quick sale, sale screen, categories]
---

# Quick Buttons

Arrange the quick-sale buttons and product grid shown on the sale screen. Any item already in your [Catalog](/help/catalog) can become a quick button; Quick Buttons doesn't create items, it just picks which ones get a tile, which category tab they sit under, and what order they appear in. Managers and admins only: a cashier doesn't see Quick Buttons in the menu at all, and adding, removing or reordering a button from the sale screen's own edit mode asks for a manager's PIN first.

## How to use it

1. Open Quick Buttons from the menu. What you see is a live copy of the sale screen's own product panel — the same category tabs and the same tiles, at the same size cashiers get — and you edit it in place. Every change shows up in the copy straight away; there is nothing to save afterwards.
2. **Add a button:** type into the search box below the panel — at least 3 characters (name, SKU or barcode); shorter than that shows a "Type 3+ characters" hint instead of searching. Matching items appear in a dropdown; tap one and its tile appears in the panel under the item's category. If nothing matches, the dropdown says "No matches." — try a shorter or different search, or check the item exists and is active in the Catalog first.
3. **Reorder and remove:** tap the pencil at the end of the category strip (or hold a tile, or right-click it) and the panel goes into edit mode: the tiles wobble, and so do the category tabs. Drag a tile to move it within its category, or drag a category tab along the strip to change the order of the tabs. By keyboard, Tab to a tile or a category tab and press the left/right arrow keys to move it. The small bin badge on a tile takes it off the quick buttons (the item stays in your Catalog), and the pencil badge opens it in the Catalog. Tap **Done** (or press Escape) when you're finished — that's when the new order is saved.
4. **Edit categories:** in edit mode every category tab shows a small pencil. Tap it to rename the category or pick a colour for its tab; **Save** applies it to the copy immediately. The bin icon in the same popover deactivates the category — you're asked to confirm first, and a category that still has active items in it is refused with the count so nothing is lost by accident (move or deactivate those items in the Catalog first, or reactivate the category later from [Categories](/help/categories)). The **+** at the end of the strip adds a new category, which appears as an empty tab ready to fill.

## Good to know

- Quick-sale buttons and categories are shop-wide and always managed from the **main till**: on a joined till, adding, removing or reordering a button or editing a category shows a message pointing you back to the main till, rather than accepting a change that would only apply locally.
- A category with no quick buttons yet shows only in Quick Buttons (with a "No quick buttons in this category yet" note) — the sale screen hides empty categories until a button lands in them.
- If the till refuses a reorder (for example, a server error), the panel reloads to show the real, saved order rather than leave a tile or tab sitting in a position that never actually took. If your connection drops entirely instead, the message says so and the panel reloads once it can.
- This is not the same page as the **Receipt designer** (Settings → Receipt printer → Receipt designer), which customises what prints on receipts — see [Receipt designer](/help/designer).
- The same edit mode exists on the sale screen itself: hold a tile (or right-click it) there and the grid wobbles the same way, with the same drag-to-reorder and edit/bin badges — the tiles only, categories are edited here (see **Selling & checkout → Rearranging the quick buttons**). A cashier can enter that edit mode too — dragging to reorder or tapping the bin badge asks for a manager's PIN on the spot before it actually saves, the same [manager-approval prompt](/help/elevation) used elsewhere.
