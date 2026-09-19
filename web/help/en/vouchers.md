---
id: vouchers
title: Gift vouchers
section: Everyday selling
order: 25
summary: Sell multi-purpose gift vouchers from the payment panel's Split tab and take them as payment — check the balance before you commit, and the receipt prints every code you issued.
keywords: [voucher, gift voucher, gift card, gutschein, einzweck, single-purpose, specific item, redeem, balance, issue, code]
---

# Gift vouchers

A gift voucher is money a customer pays now to spend later — on anything in the shop, at any till. The till keeps each voucher's balance under its **code**: the one printed on a physical card you hand over, or one the till makes up when you leave the code blank. Both selling a voucher and taking one as payment happen in the payment panel's **Split** tab (see [Payments](/help/payments) for the tab itself).

## Selling a voucher

1. Tap **Payment**, switch to the **Split** tab, and open **Sell a voucher** at the bottom of the panel.
2. Type the voucher's value in **Voucher amount** — a normal currency amount, e.g. `25.00`.
3. **Code**: type the code printed on the card you're handing over. Leave it blank and the till generates a code at checkout instead.
4. **For** is optional — a name to remember who the voucher was bought for.
5. **Add Voucher**. It joins a pending list showing its code (or *Code generated at checkout*), value and holder, each with a ✕ to remove it. The value is added to what the customer owes — **Fill Remaining** counts it, so the arithmetic works the same as any other sale.
6. Take the payment as usual (cash, card, …) and **Complete Sale**. The receipt lists every voucher under **Vouchers issued** with its final code. For a generated code this receipt is the only place the code is ever shown, so hand it to the customer or write the code on the card.

A voucher can be sold on its own, with nothing else in the basket, or together with goods in one sale. Selling a voucher is not product revenue: it's recorded as money owed to whoever later spends it, and the goods are taxed when the voucher is spent — see the day-end notes below.

What can go wrong:

- A value of zero is refused before it's added.
- A code that's already in use is refused when you press **Complete Sale**, with a message saying so — the pending list stays, so correct the code and try again.
- One sale can issue up to 50 vouchers.

## Taking a voucher as payment

1. In the **Split** tab, choose **voucher** as the method. A **Voucher code** box appears, and the Change box disappears — a voucher never gives change.
2. Type the code from the customer's card.
3. **Check balance** (optional) looks the voucher up and shows its remaining balance before you commit. If the Amount box is still empty it also fills in the natural amount — the balance, or what's still owed if that's less. If the till can't reach the main till right now, the check simply says so; you can still add the payment, and the real check happens when the sale completes.
4. Enter the **Amount** to take from the voucher (up to its balance, and no more than what the sale still needs), then **Add Payment**. Pay any remainder with another method, then **Complete Sale**. The receipt shows the voucher code on that payment.

What can go wrong:

- **Add Payment** with the code box empty is refused.
- A code the till doesn't know, or a voucher that has been cancelled or fully spent, is refused — at **Check balance** if you use it, otherwise when the sale completes.
- An amount above the voucher's balance is refused ("Voucher balance does not cover this payment") — take what's left on the voucher and the rest another way.
- An amount above what the sale still needs is refused too, since a voucher can't give change — enter the remaining amount instead.
- A voucher can be used once per sale, and a voucher being sold in this same sale can't pay for it.

The older **gift** method in the same list is a plain payment method that doesn't track a balance — use **voucher** for vouchers the till issued.

## Vouchers for a specific item

Most gift vouchers are for **any use** — the customer decides later what to spend them on, and the goods are taxed when the voucher is spent. A voucher sold for a **specific item** (a particular product or service, so its VAT rate is already known — in Germany an *Einzweck-Gutschein*) is taxed when it is sold instead, and redeeming it later is simply handing the item over. The till keeps the two kinds apart.

Selling one:

1. In **Sell a voucher**, choose **Specific item (taxed now)** under **Voucher type** (the default, **Any use**, is the everyday voucher described above).
2. A **VAT rate** box appears — type the rate the item carries as a percentage, e.g. `19` or `7`.
3. Fill in the amount, code and holder as usual and **Add Voucher**. The pending list marks it *Specific item* with its rate. The sale records the VAT at that rate straight away, so the voucher shows in the day's VAT bands like a normal sale line — not in the **GUTSCHEINE** liability section.

Redeeming one:

1. In the **Split** tab, choose **voucher** as the method and type the code, then **Check balance**.
2. For a specific-item voucher the till does not fill in an amount — it shows the voucher's value and holder next to a **Redeem** button instead. Such a voucher can never be used as a payment: it has already been taxed, so taking it as payment would tax the same money twice.
3. Hand over the item and tap **Redeem**. This is final: the voucher is marked as used at once, with nothing to complete or remove afterwards. No further payment or tax is recorded — the sale that sold the voucher already did that.

What can go wrong:

- A VAT rate that is empty, negative or above 100 is refused before the voucher is added.
- Tapping **Redeem** on a voucher that has already been used, or scanning a specific-item voucher at the sale screen, is refused with a message saying so.
- A specific-item voucher sold on another till can be checked from this one but not yet redeemed here — redeem it on the main till or on the till that sold it.

## Day-end reports and other tills

The day-end (Z) report has its own **GUTSCHEINE** section for vouchers sold and spent that day, and voiding a sale that sold a voucher cancels the voucher with it (only while it's unused) — see [Reports & end of day](/help/reports). A voucher sold on one till can be spent on any other till in the shop; how that works while a till is offline is covered in [Multiple tills (one shop)](/help/multitill).
