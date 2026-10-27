---
id: customer-order-tracking
title: Customer order tracking (QR)
section: Everyday selling
order: 46
summary: "Customers scan a QR code, on the self-order kiosk's confirmation or on the receipt of a sale rung up at the till, to follow their order's status on their own phone. Part of the Shop, Pro and Multi-location plans."
routes: [/o/{token}, /o/{token}/status]
keywords: [tracking, QR, customer, order status, self order, phone, receipt, counter]
---

# Customer order tracking (QR)

Customers scan a QR code to follow their order's status on their own phone: after ordering at the self-order kiosk (whether they paid there or will pay at the counter), or from the receipt of a sale your staff ring up at the till. Like the self-order screen itself, the page the customer sees is customer-facing, so it doesn't carry a "?" link back to this manual — find this topic from the manual's search or topic list.

**Not available yet:** order tracking is moving to our cloud. Until it does, the till shows no tracking QR anywhere — not on the kiosk confirmation, not on receipts — because a link to the till itself only works on the shop's own Wi-Fi and would hand every customer the till's address. The rest of this page describes how it will work once the cloud page is live.

## How it works

1. When a customer completes an order at the self-order kiosk, the confirmation screen shows a QR code next to the order number (the confirmation stays up for about 20 seconds, long enough to scan).
2. When your staff ring up a sale at the till, the receipt carries a QR code with the line **Scan to track your order**: on the printed receipt and on the receipt shown on screen after payment. Reprinting the receipt prints the same QR. This depends on the receipt setting described in the notes below.
3. Scanning it opens a small page on the customer's phone showing the order number and its current status — the same statuses your staff set on the **Orders** screen: preparing, ready, collected.
4. The page updates itself every few seconds, so the customer sees "Ready" the moment your staff tap it — no reloading, no asking at the counter.
5. The link opens in the language the kiosk was being used in. A printed receipt's link opens in the shop's language; the receipt shown on screen after payment uses the till screen's language.
6. If the customer chose to pay at the counter, the page shows **Waiting for payment at the counter** until your staff take payment for the order. From then on it shows the order's status like any other order. An order left unpaid for more than about 2 hours stops showing on the page until it is paid.

## What the customer can and can't see

- The page shows **only the order number and its status** — no names, no items, no prices, no payment details. The link is a long random code that can't be guessed, and each one belongs to a single order.
- Once the order is collected (or cancelled), the link keeps working for about 2 hours and then stops answering — an old receipt's QR doesn't stay live forever.

## Notes

- Order tracking is part of the Shop, Pro and Multi-location plans. On the free Local plan, on Local Plus, or while paid features are paused (the subscription ended, or the till couldn't confirm it with the cloud for more than 7 days), the confirmation and receipts show without a QR; the order or sale itself is never affected.
- The tracking page will be hosted in our cloud, so a customer's phone won't need your shop's Wi-Fi, and the till's own network address never appears on a QR code or a receipt.
- If your shop has several tills and the order is paid on a different till from the kiosk, the kiosk's link can't see the payment: it keeps showing **Waiting for payment at the counter** for up to about 2 hours, then stops answering. Until order tracking moves to our cloud, take payment for kiosk orders on the kiosk's own till to avoid this.
- The **Order tracking QR (Shop plan and up)** box in the [Receipt designer](/help/designer) turns the QR on till receipts on or off. Until you first save the receipt designer, it is on, unless your shop doesn't ask "dine in or takeaway" (retail and service shops start with that question switched off), in which case it is off. Saving the designer keeps the box as it is shown. Receipts of refunds and voided sales never carry a QR, and neither do receipts from an additional till in a shop with several tills: the main till's **Orders** screen can't see that till's tracking link until order tracking moves to our cloud.
- An order nobody tracks on the **Orders** screen (the usual case for an everyday sale at the till) stops answering 12 hours after the sale. Once your staff set its status, the rules above apply instead.
