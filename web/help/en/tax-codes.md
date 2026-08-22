---
id: tax-codes
title: Tax codes
section: Setting up your shop
order: 111
summary: "Create and edit tax codes — the dine-in rate and any takeaway rate an item is charged — and activate or deactivate them."
routes: [/catalog/tax-codes]
keywords: [tax, vat, rate, takeaway, tax code]
---

# Tax codes

Every item in your catalog is linked to a tax code, which sets the tax rate charged on it (and, optionally, a different rate for takeaway orders). This page is where those codes are created and edited by hand — separate from Catalog, which only lets you pick an existing code for an item.

## How to use it

1. Open **Catalog → Tax codes** (manager only). The list shows every tax code, active and inactive, with its dine-in rate and any takeaway rate.
2. Click a row to load it into the form on the right, edit the name, rate or takeaway rate, and press **Save**.
3. Fill in the form without clicking a row first to create a brand new tax code. A new code is always active.
4. Use **Deactivate**/**Reactivate** to retire or restore a code instead of deleting it — items already linked to a tax code keep their link even after it's deactivated, so historical sales stay correct. There is no delete: a tax code can be referenced by items, so removing it outright isn't offered.
5. Rates are entered as a percentage — enter `19` for 19%. Leave the takeaway rate blank if it doesn't differ from the dine-in rate.
6. If a plugin (e.g. the German tax add-on) needs a takeaway override that applies only to that plugin's own logic rather than every item on a tax code, use the **Manage per-plugin overrides** link, which opens that plugin's own settings page.
