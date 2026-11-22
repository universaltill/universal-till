---
id: channel-orders
title: Online orders
section: Everyday selling
order: 16
public: true
summary: "Orders taken on your web shop or marketplaces and recorded on this till: what was ordered, how it is progressing and what it did to your stock."
routes: [/channel-orders, /channel-orders/{id}]
keywords: [online orders, web shop, webshop, marketplace, channel, shopify, order number, refund, restock, stock, not a sale, accept, pick, pack, ship, tracking, cancel, discount, discount code, promotion, campaign]
---

# Online orders

Orders taken on your web shop or marketplaces and recorded on this till: what was ordered, how it is progressing and what it did to your stock. They are not till sales.

## How to use it

1. Open **Online orders** from the ☰ menu. The newest order is first. Each row shows the order number, when it was placed, the channel it came from, its status, how many lines it has and the total.
2. Tap an order number to see its lines: item, SKU, quantity, unit price, the line discount (for all its units together; the column shows only when a line has a discount), how many units were refunded and how many of those went back into stock. Below the lines, **Items** is the lines added up before discounts, **Line discounts** adds up the line discounts and **Order discount** shows a discount the shop could not put on one line, so the items, discounts, shipping and tax add up to the total. These rows show only when the order has a discount. A line marked **Not matched to an item** has no item here, so your stock was not changed for it; the till also raises a problem you can see on the back-office page.
3. Stock goes down by the quantity ordered when the order is recorded, even if that takes an item below zero (the till warns you). A refund that the channel marks as restocked puts those units back. Customer details are never stored: only the customer's country is shown.
4. **Accept an order.** The **Actions** block is at the top of the order. A new order offers **Accept order** (accepting is your promise to the online shop) and **Cancel order…**. Tap **Accept order**.
5. **Pick and pack.** **Mark as picked** and **Mark as packed** are optional; use them to keep the shop informed. Once an order is accepted you can also skip straight to shipping.
6. **Ship it.** **Ship order…** opens a small form: enter the carrier and, if you have them, the tracking number and the tracking link (a full web address starting with https://). Then tap **Mark as shipped**.
7. **Cancel it.** **Cancel order…** opens a form: choose a reason, decide whether the items go back into stock (ticked by default) and tap **Cancel this order**. This also cancels the order at the online shop and cannot be undone. A shipped order cannot be cancelled.
8. **Waiting to be sent.** After a tap, a dashed chip such as **Shipping not sent yet** appears and the status stays as it was. The action is saved on this till and sent to the online shop as soon as the till is online. The status changes when the shop confirms it, and stock changes only then.
9. **If the shop says no.** A notice tells you why (for example, the order was already cancelled) and the order shows **Not accepted** in the list. Tap **Dismiss** once you have read it. If the online shop cannot take a shipment or a cancel automatically, the notice asks you to make the same change in the online shop's own admin.

## Notes

- These orders are **not till sales**. They are not in the drawer, the Z report, the Journal or the fiscal journal; the shop took and was paid for them online. To see totals per channel for a period, open the Online channels tab in Reports on the main till.
- Discounts, discount codes, automatic discounts and sales or campaigns that you set up in your online shop are not copied to the till, and promotions you set up on the till are not sent to the online shop. Each order brings only the discount the customer actually got: on each line, plus any order discount that could not be put on a line. The total is what the customer paid.
- Only the main till records online orders and takes actions on them. People who may see Reports can open this page; taking actions also needs permission to manage stock. On an additional till the page is view-only.
- If the till is offline, orders wait in the cloud and are recorded the next time it connects, and the actions you take wait on the till and are sent when it reconnects. Selling at the till is never held up by this.
- If this till stops being the main till, actions that were not sent yet stay on the page with a notice until you dismiss them. They are never thrown away silently.
