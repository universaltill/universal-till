---
id: categories
title: Categories
section: Setting up your shop
order: 114
summary: Group your catalog into categories — the tabs on the sale screen and the department a product lists under — and create, rename, reorder or deactivate them here.
keywords: [category, categories, department, sale screen, tabs, kitchen routing, search]
routes: [/categories]
---

# Categories

Categories group your catalog — they're the tabs across the top of the sale screen, the **Category** field on a catalog item, and what kitchen-station routing sorts by. This page is where you create, rename, reorder or deactivate them.

## How to use it

1. Open **Categories** from the menu (manager only). Every category shows its name, how many active items currently use it, and whether it's active.
2. Tap the **+** button beside the search box to add a category. A full-screen form opens: type the name, optionally pick a **colour** (the same fixed palette as an item's tile colour — it shows on the category's sale-screen tab and beside its name in this list), optionally pick an **image** (see below), tick any **customization groups** every item in the category should offer, and tick the **kitchen stations** its items should print at, then tap **Save**. The new category appears at the end of the list and is immediately available on the catalog item editor's Category field.
3. Tap a category's row to edit it (with a keyboard, Tab to its name and press Enter). The same form opens with the name, colour, image, groups and stations filled in — change what you need and tap **Save**; every item already in the category keeps its assignment, nothing else changes. Each row starts with the category's image or icon — or its colour, if it has neither — so you can spot it at a glance.
4. To reorder the list, press and hold a category's row for about half a second until it lifts, then drag it up or down and let go — the new order is saved as soon as you drop it (press Escape while dragging to cancel). With a keyboard, Tab to a category's name and press Alt+↑ or Alt+↓; or open a category and use **Move up** / **Move down** in the form. This is also the order categories appear as tabs on the sale screen and rows on the kitchen-routing page.
5. To retire a category you no longer use, open it and tap the bin button at the top of the form. It is deactivated, not deleted: it disappears from the sale-screen tabs and the kitchen-routing grid, but keeps showing in the catalog item editor's Category field — so an item still assigned to it never loses that assignment — and its row stays here. To bring it back, open it and tap **Activate**.

## Good to know

- Type in the search box to filter the list as you type. If nothing matches, the list says so — clear the search to see every category again.
- Closing the form with unsaved changes asks you first, so a stray tap on Close never throws away what you typed.
- A category with active items still in it can't be deactivated — the message tells you how many items are in the way. Move or deactivate those items first (or reassign them to a different category from the catalog page), then deactivate the category.
- **Customization groups on a category are inherited:** every item in the category offers them when it's added to a sale, alongside any groups attached to the item itself, with no per-item copying — change the category's groups and every item follows immediately. An item can skip one of the inherited groups from its own editor (Items → the item → Modifiers tab → *Skip for this item*), which affects only that item. Groups themselves are created under **Modifiers**; this page only picks from the ones that already exist.
- **Kitchen routing follows the same rule the Kitchen stations page uses:** an item prints at its own stations if it has any, otherwise at its category's stations, otherwise at the default kitchen printer. Ticking stations here is the same as ticking the category's row on the Kitchen stations page; an item's own override is set from its editor's Variants tab (or the Kitchen stations page) and always wins outright — it doesn't add to the category's stations.
- **Category images:** a category's image shows on its tile on the sell screen (the *Category tiles* browsing mode), on its tab in the category strip, and in the **…** list of all categories. In the form, tap one of the built-in images (the same library of about 70 as the item editor, with a **Search icons** box), upload a photo with **Take a Photo** or **Choose File** (PNG or JPEG), or tap **No image** — a category has one picture, so a built-in image and an uploaded photo replace each other. Category tiles stay compact either way: the image on top, the name and item count below. A joined till shows the same picture: a built-in image straight away, and a photo uploaded on the main till within about half a minute.
- **Hidden from the sale screen:** untick **Show on the sale screen** in the form to keep a category and its subcategories off the sale screen's category tabs and the **…** list — useful for items you only ever scan or search for. Its items can still be sold by search and barcode scan, its quick buttons move to the **Uncategorized** tab, and in the **All items with category filters** layout they still show in the All grid; and this list marks the category *Hidden on sale screen*. The same setting can be changed from the shop's cloud manage page, which can also give a category an icon from the same library. A category still has one picture: an icon set there replaces the image or photo picked here, and picking one here replaces that icon — whichever was set last shows. An icon this till doesn't know yet shows as a plain tag.
- Categories are a flat list — there's no nesting or sub-categories here.
- Categories are shop-wide and always managed from the **main till**: on a joined till, creating, renaming, reordering or deactivating a category shows a message pointing you back to the main till, rather than accepting a change that would only apply locally.
- Deleting a category outright isn't offered from this page — deactivate instead. A category is only ever removed for good when it's deleted on the main till and no other till still needs it.
