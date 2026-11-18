---
id: data-management
title: Data management
section: Running the business
order: 252
public: true
summary: Clear practice sales before you go live, restore or delete a reset, erase a customer's personal details on request, clean up the catalog, remove sample data and run an export.
keywords: [data, reset, clear, transactions, archive, restore, purge, gdpr, erase, delete customer, privacy, catalog cleanup, sample data, export]
---

# Data management

![Data management card](img/data-management-card.png)

Settings → Data management is where a manager tidies the till's records: wipe the practice sales you made while testing, bring a wiped batch back, erase a customer's personal details when they ask, remove products you never sold, and run an export. Clearing, restoring or deleting an archive, erasing a customer, catalog cleanup and removing sample data are recorded in the audit log (running an export is not), and most ask for a manager PIN first, even if you are already signed in as a manager.

The card is shown to managers. A cashier only sees it while sample data or a country plugin that is still installing is waiting, and then only those parts.

## Clear transaction history

**When:** once, before you start trading for real, to wipe the practice sales from testing. It is not something to do routinely.

1. Settings → Data management → **Clear transaction history**.
2. Enter your manager PIN.

This clears **all** sales, shifts and invoices from the working till, not only the test ones. Parked (held) orders, payments and the stock-movement history are cleared too, and archived with the batch, so close any real parked order or table tab first. Your products, users, settings and earlier reports stay. Nothing is destroyed: the cleared records move into a dated reset archive (next section).

**What can go wrong:** if you cleared more than you meant to, restore the batch from Reset archives. That only works until the till takes a new sale.

## Reset archives

Each time you clear the transaction history, that batch is listed under **Reset archives** instead of vanishing.

- **Restore** puts the batch's sales, shifts and invoices back exactly as they were. It works only while the till has taken no new transactions since the reset, and only if nothing the batch refers to (a product, stock location or customer) was removed afterwards, for example by sample-data removal, catalog cleanup or erasing a customer.
- **Delete permanently** removes a batch for good. A batch holding real trading history is protected by your country's retention rules: instead of the button you see the date from which it can be deleted.

Both ask for a manager PIN.

This is not the same as restoring a backup. A reset archive undoes a clear; a backup replaces the whole till's data with an older copy. See [Backups](/help/backups).

## Erase a customer (GDPR)

**When:** a customer asks you to delete their personal details.

1. Settings → Data management → **Erase a customer (GDPR)**.
2. Type the customer's name, phone or email in the search box and press **Search**.
3. Press **Erase** next to the right customer and enter your manager PIN.

**What happens:**

- The customer's record is deleted.
- Their past sales are kept for your records, but no longer show who they belonged to. This also applies to sales in reset archives.
- Parked orders lose the customer's name.
- A promotion set up for that customer is switched off.
- Every other till in the shop removes the customer's details the next time it syncs with the main till.
- Plugins that keep their own copy of customers, such as a loyalty plugin, are told about the erasure and are expected to delete their copy. The till sends the notice to each enabled plugin that is allowed to receive it; it cannot check that the plugin acted on it. A plugin that is switched off at the time is not told.
- The erasure is recorded in the audit log.

**What is not changed:**

- Invoices already issued to that customer keep the buyer's name, address and VAT number exactly as issued, because tax law requires you to keep invoices.
- **Backups.** A backup made before the erasure still contains the customer until it ages out. By default the till keeps 7 days of backups, longer if you raised that number, and any copy you saved off the till stays as it is. The copy of your data the till set aside when you last restored a backup also keeps them, for as long as sales records must be kept. Restoring an older backup brings the customer back, so erase them again afterwards. See [Backups](/help/backups).

The till has no button that exports one customer's data.

## Catalog cleanup

**When:** you imported the wrong catalog, or want to tidy away products you never used.

1. Settings → Data management → **Catalog cleanup**.
2. Press **Preview** to see what would be removed. Nothing is removed yet.
3. Press **Remove** and confirm with your manager PIN. The PIN prompt says how many products will go.

This permanently removes inactive products that were never sold and have no stock history. A product that was ever sold, voided, comped, written off as waste, refused at an ID check or is in a parked sale always stays, for your records. Tick **Also include active products that were never sold** to widen it to active, never-sold products, useful for clearing a wrongly imported catalog before you go live.

## Sample data

If you loaded sample data from the Import page, the **Sample data** section appears and shows how many sample records the till holds.

1. Press **Remove sample data** and confirm.

This removes the sample items, the sample customers and the sample promo codes together, so a discount code from the demo cannot be used later. If the till has not taken a real sale yet, everything sample is removed, even a sample item you renamed or repriced while trying things out.

Once the till has taken a real sale, an item or promo code you edited counts as yours. It is listed separately with the reason, and offers **Remove anyway** or a keep button (**Keep as my own item** / **Keep as my own**). A sample customer or promo code that is in use, meaning sold to, in a parked sale or targeted by a promotion, is listed too but stays, and so does an item that was sold or had stock adjusted. For items, use Catalog cleanup after deactivating them in the catalog. If a sample item or customer is in a basket (cashier or self-order), clear the basket first.

## Export / report

Under **Export / report** you can run an export or report plugin that is installed on this till, for example a tax-audit export.

1. Pick a **From** and **To** date, at most 366 days apart.
2. Choose the export and press **Run export**.

Files download straight to this device. Some exports only confirm that they were sent. If it says **No export or report plugin is installed**, add one from the marketplace; see [Plugins](/help/plugins) and, for tax-audit exports, [Fiscal register](/help/fiscal-register). This is not a personal-data export for a customer.
