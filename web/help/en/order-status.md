---
id: order-status
title: Order status (kitchen progress)
section: Everyday selling
order: 45
summary: "Mark each order's progress — preparing, ready, collected — with one tap, so everyone can see where an order is."
routes: [/orders]
keywords: [orders, kitchen, preparing, ready, collected, cancelled, status]
---

# Order status (kitchen progress)

Mark each order's progress — preparing, ready, collected — with one tap, so everyone can see where an order is.

## How to use it

1. Open **Orders** from the menu. Recent sales are listed newest first, each with its current status.
2. Tap **Preparing** when the kitchen starts on an order, **Ready** when it can be picked up, and **Collected** when the customer has it. Any operator can do this — no manager PIN needed.
3. Every change records who made it and when, next to the status. The receipt number links to the full receipt in the Journal.
4. **Cancel order** marks an order cancelled. It works at any point until the order is collected; a collected order can't be cancelled.
5. An order's status only ever moves forward. Tapping an earlier step by mistake (or a second till reporting an old status after being offline) changes nothing — the till just keeps showing the later status. That's deliberate, not a fault.
6. Orders from sales made before this feature (or that nobody has tapped yet) show **Not started** — nothing changes for them until someone taps a status.

## Notes

- The list refreshes itself every few seconds — new orders (including ones placed on the self-order kiosk) appear without reloading the page.
- If a kitchen ticket or receipt could not be printed — a printer out of paper, unplugged, or offline — the order shows a ⚠ warning next to its status, so a paid order (for example from the self-order kiosk) is never silently lost. A kitchen ⚠ means the kitchen never received the ticket: fix the printer and pass the order to them yourself. A receipt ⚠ clears as soon as you fix the printer and reprint that receipt from the order's page in the Journal.
- Everything here works fully offline, like the rest of the till.
- This screen is the foundation for what's coming next: a kitchen display, customer pagers and order-tracking will all follow the same statuses.
