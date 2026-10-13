---
id: customer-order-tracking
title: Customer order tracking (QR)
section: Everyday selling
order: 46
summary: "After ordering at the self-order kiosk, customers scan a QR code to follow their order's status on their own phone. Part of the paid plans."
routes: [/o/{token}, /o/{token}/status]
keywords: [tracking, QR, customer, order status, self order, phone]
---

# Customer order tracking (QR)

After ordering at the self-order kiosk, whether they paid there or will pay at the counter, customers scan a QR code to follow their order's status on their own phone. Like the self-order screen itself, the page the customer sees is customer-facing, so it doesn't carry a "?" link back to this manual — find this topic from the manual's search or topic list.

## How it works

1. When a customer completes an order at the self-order kiosk, the confirmation screen shows a QR code next to the order number (the confirmation stays up for about 20 seconds, long enough to scan).
2. Scanning it opens a small page on the customer's phone showing the order number and its current status — the same statuses your staff set on the **Orders** screen: preparing, ready, collected.
3. The page updates itself every few seconds, so the customer sees "Ready" the moment your staff tap it — no reloading, no asking at the counter.
4. The link opens in the language the kiosk was being used in.
5. If the customer chose to pay at the counter, the page shows **Waiting for payment at the counter** until your staff take payment for the order. From then on it shows the order's status like any other order. An order left unpaid for more than about 2 hours stops showing on the page until it is paid.

## What the customer can and can't see

- The page shows **only the order number and its status** — no names, no items, no prices, no payment details. The link is a long random code that can't be guessed, and each one belongs to a single order.
- Once the order is collected (or cancelled), the link keeps working for about 2 hours and then stops answering — an old receipt's QR doesn't stay live forever.

## Notes

- Order tracking is part of the paid plans. On the free Local plan, or while paid features are paused (the subscription ended, or the till couldn't confirm it with the cloud for more than 7 days), the confirmation shows without a QR; the order itself is never affected.
- The customer's phone must be on the same network as the till — the shop's Wi-Fi. If the till has no network address other than itself, the confirmation simply shows without a QR; the order itself is never affected.
- If your shop has several tills and the order is paid on a different till from the kiosk, the kiosk's link can't see the payment: it keeps showing **Waiting for payment at the counter** for up to about 2 hours, then stops answering. Until order tracking moves to our cloud, take payment for kiosk orders on the kiosk's own till to avoid this.
- The QR appears only for orders placed at the self-order kiosk. Sales your staff ring up at the till don't print a tracking QR yet.
