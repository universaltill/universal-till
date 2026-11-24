---
id: card-readers
title: Card readers
section: Everyday selling
order: 21
public: true
summary: Set up the card readers a till can use, of any provider, so a cashier can choose which one takes each card payment.
keywords: [card reader, terminal, readers, reader id, provider, stripe, sumup, self-order reader, which reader]
routes: [/settings/card-readers]
---

# Card readers

Set up the card readers a till can use, of any provider, so a cashier can choose which one takes each card payment. The list is saved on this till only: a reader's ID belongs to a device, so another till has its own list.

## How to use it

1. Open **Settings → Payments** and tap **Card readers**. Only a manager or admin can; a cashier only sees the choice at the till.
2. To add a reader, enter a **Reader name** staff will recognise (for example "Front counter", up to 40 characters), choose its **Provider**, and enter its **Reader ID** exactly as your provider shows it in its dashboard or on the reader (letters, numbers and `. _ : -` only, starting with a letter or a number). Tap **Add reader**.
3. To change the order the cashier sees, use **Move up** and **Move down**. To rename a reader, change its name and tap **Rename**.
4. If a reader is the one the self-order screen should use, tick **Use for the self-order kiosk**. Only one reader can have it. The self-order screen never shows a list of readers: with one reader it uses that one, with several it uses this one, and with several and none ticked it says card payment is not available and asks the customer to see staff.
5. To remove a reader, tap **Remove reader** and confirm. Past sales and refunds are not affected.

A till that already had a reader ID saved in its payment plugin's settings gets that reader added automatically (tagged **Imported from plugin settings**); the plugin's own setting is left as it was. If you remove that reader, the saved ID in the plugin's settings is cleared as well, so card payments do not fall back to the reader you just removed; the confirmation says so.

Only payment plugins that support several readers appear in **Provider**. A reader of an older plugin is listed with a note that the plugin needs an update; it keeps working as before until you update it.

Using a phone as a card reader is available on the main till only. A till holds at most 32 readers. Readers restored from a backup are kept but must be checked before use, because reader IDs are device specific.

Every card reader needs an internet connection to take a payment; the **Which reader?** list at the till says so under each reader.

## What can go wrong

- **That reader ID is not valid or is already added** — the ID has a character that is not allowed, is empty, or is already in the list.
- **A payment is still using this reader. Finish that payment first, then remove the reader** — a card payment is running on that reader, or one that could not be confirmed is waiting to be tried again on it. Finish it at the till, then remove the reader.
- **Enter a name of 1 to 40 characters that no other reader uses** — pick another name.
- **No installed payment plugin supports several readers** — install or update a payment plugin from the store first.

See [Payments](/help/payments) for how a cashier chooses a reader at the till.
