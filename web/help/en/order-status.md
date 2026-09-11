---
id: order-status
title: Order status (kitchen progress)
section: Everyday selling
order: 45
summary: "Mark each order's progress — preparing, ready, collected — with one tap, so everyone can see where an order is."
routes: [/orders, /orders/{receipt}]
keywords: [orders, kitchen, preparing, ready, collected, cancelled, status, scan, collect]
---

# Order status (kitchen progress)

Mark each order's progress — preparing, ready, collected — with one tap, so everyone can see where an order is.

## How to use it

1. Open **Order status** — the 🛎️ icon in the rail on the sale screen, or from the ☰ menu. This is the *active* queue: recent, still-in-progress sales are listed newest first, each with its current status.
2. Scanning a customer's receipt barcode at the sale screen also opens their order directly, if it hasn't been collected yet — with the same one-tap **Collected** button, and a link to Refund if that's what they actually need instead. A receipt that's already collected, or was never tracked, opens Refund directly, exactly as scanning any other finished receipt does; a cancelled order, or a sale that never completed, just says so on the sale screen.
3. Tap **Preparing** when the kitchen starts on an order, **Ready** when it can be picked up, and **Collected** when the customer has it. Any operator can do this — no manager PIN needed.
4. Every change records who made it and when, next to the status. The number shown is the short order number your customer was given (see below) — tapping it opens the full receipt in the Journal, except for an order shown here because it was taken on a *different* till (see the note below), where the number is shown but isn't a link, since this till doesn't hold that receipt itself.
5. **Cancel order** marks an order cancelled. It works at any point until the order is collected; a collected order can't be cancelled.
6. Marking an order **Collected**, or cancelling it, removes it from this list right away — it's done, so it stops taking up space in the active queue. Nothing is deleted: the receipt, every status change, and who did what stay exactly where they always did, in the Journal.
7. An order's status only ever moves forward (Collected and Cancelled are the end of the line). Tapping an earlier step by mistake (or a second till reporting an old status after being offline) changes nothing — the till just keeps showing the later status. That's deliberate, not a fault.
8. Orders from sales made before this feature (or that nobody has tapped yet) show **Not started** — nothing changes for them until someone taps a status.

## Notes

- The list refreshes itself every few seconds — new orders (including ones placed on the self-order kiosk) appear without reloading the page.
- If a kitchen ticket or receipt could not be printed — a printer out of paper, unplugged, or offline — the order shows a ⚠ warning next to its status, so a paid order (for example from the self-order kiosk) is never silently lost. A kitchen ⚠ means the kitchen never received the ticket: fix the printer, then press **Resend kitchen ticket** next to the warning to send it again — no need to leave this board. A receipt ⚠ clears as soon as you fix the printer and reprint that receipt from the order's page in the Journal.
- When several tills are linked, every till shows and updates the whole shop's orders through the main till while it is reachable — not just the till that took the order. A till that can't reach the main till keeps working with its own local list, and shows the shared board again as soon as the connection is back — but a status change made *while disconnected* isn't sent anywhere else: as soon as that till reconnects, its screen switches back to the shared board and any tap made while it was offline can visibly disappear from that screen (a manager who taps Ready on a genuinely offline till should double-check it once the till reconnects). A brand-new order also doesn't appear on the shared board — even on the till that just took it — until it's finished syncing to the main till, normally a few seconds.
- Everything here works fully offline, like the rest of the till.
- This screen is the foundation for what's coming next: a kitchen display, customer pagers and order-tracking will all follow the same statuses.

## Order numbers

Every sale keeps its permanent receipt number (used for refunds and reports) — but customers, the kitchen ticket, the self-order confirmation screen, and this list show a **short order number** instead, so it's easy to read out or call across the counter. In **Settings → Order numbers**, choose whether it resets to 1 after each till close (the default — matches what most modern POS systems do) or just keeps counting without ever resetting. Changing it only affects new sales; past receipts are unaffected.
