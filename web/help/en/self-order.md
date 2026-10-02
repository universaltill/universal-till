---
id: self-order
title: Self-order
section: Everyday selling
order: 40
summary: A customer-facing mode where shoppers browse the catalog and build their own order.
keywords: [self order, kiosk, customer]
---

# Self-order

Self-order turns a till into a customer-facing screen where shoppers browse the catalog and build their own order themselves. That screen is for the customer, not the operator, so it doesn't carry a "?" link back to this manual — find this topic from the manual's own search or topic list instead. A full setup guide will be added to this manual soon.

To make a till the kiosk, a manager picks Self-order kiosk under Settings → Display → Device profile, or gives the Self-order kiosk user a PIN on the Users page and signs in with that PIN. Either way the till switches to self-order mode and stays in it after a restart. Tapping the lock on the kiosk screen and signing in with any other user's PIN switches the till back to normal till mode and opens the sale screen.

Shoppers find items by tapping category chips — there is no search box on this screen, by design.

The cart carries the same dine-in/takeaway toggle as the cashier's basket, so a self-order customer can mark their own order takeaway (or switch back). This records the choice on the receipt, journal and kitchen ticket; it only changes the tax charged if a tax plugin for your region uses it to pick a different rate. The cart's total is exactly what the kiosk charges: if you set a service charge in Settings, or a plugin for your region adds a levy, the cart shows it and the payment includes it — the same as a sale at the till.

By default the kiosk takes payment itself (card/contactless) before sending an order to the kitchen. Settings → Kiosk payment mode also offers **Pay at counter**: the kiosk takes no payment, shows the customer a short order number such as **C-12**, and the order waits on the till like a held sale — in **Open orders** — until a cashier brings it up and takes payment. It becomes a normal sale then, and its kitchen ticket prints at that moment. See [Open orders](/help/open-orders).
