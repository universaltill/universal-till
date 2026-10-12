---
id: vouchers
title: Gift vouchers
section: Everyday selling
order: 25
summary: Sell gift vouchers by tapping a Gift voucher item — scan the card or let the till create a number — and take them as payment by scanning them at the sale screen or in the payment panel's Split tab.
keywords: [voucher, gift voucher, gift card, gutschein, hediye çeki, einzweck, single-purpose, specific item, redeem, balance, issue, code, card number, item type]
---

# Gift vouchers

A gift voucher is money a customer pays now to spend later — on anything in the shop, at any till. The till keeps each voucher's balance under its **card number**: the one printed on a physical card you hand over, or a 13-digit number the till creates when you leave the number empty. You sell a voucher by tapping a **Gift voucher** item on the sale screen, and take one as payment by scanning it or in the payment panel's **Split** tab (see [Payments](/help/payments) for the tab itself).

## Selling a voucher

Selling needs an item whose **Type** is *Gift voucher* in the catalogue (see [Catalog](/help/catalog)) — set one up once. Leave its price empty to be asked for the value every time, or give it a price (e.g. *Gift voucher €25*) to start the popup with that value.

1. Tap the gift voucher item on the sale screen (or scan its barcode). The **Gift voucher** popup opens.
2. **Card number**: scan the card you're handing over — the field is ready for your scanner as soon as the popup opens — or type its number. Leave it empty and the till creates a number when you tap **Add**.
3. **Value**: the voucher's value, a normal currency amount, e.g. `25.00`. It starts with the item's price when the item has one.
4. **For** is optional — a name to remember who the voucher was bought for.
5. **Can be spent on**: **Any purchase** is the everyday voucher; **Specific item (taxed now)** is described below. Tap **Add**.
6. The voucher joins the basket as its own line, showing its card number. Take the payment as usual (**Payment**, cash, card, …). The receipt lists every voucher under **Vouchers issued** with its number. For a number the till created, this receipt is where the customer finds it, so hand it over or write the number on the card.

A voucher can be sold on its own, with nothing else in the basket, or together with goods in one sale. Its basket line is always one voucher at its value: it has no quantity buttons or line discount, and a promotion or basket discount never reduces it. Tap ✕ on the line to drop the voucher — no reason is asked, because nothing has left the shop. Selling a voucher is not product revenue and moves no stock: it's recorded as money owed to whoever later spends it, and the goods are taxed when the voucher is spent — see the day-end notes below. A held sale keeps its voucher lines, and the voucher is issued when the sale is paid.

What can go wrong:

- **Add** stays greyed out until the value is above zero.
- A card number that's already in use — sold before, or already on another line of this sale — is refused in the popup with a message saying so, before anything is added. Scan another card, or leave the number empty.
- One sale can sell up to 50 vouchers.

## Taking a voucher as payment

The quickest way: scan the voucher's barcode — the card, or the number printed on the receipt — on the sale screen. The till looks it up and offers a **Voucher** button in the payment panel for its balance, up to what the sale still needs. To type the number instead, or to split the payment:

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

1. In the **Gift voucher** popup, choose **Specific item (taxed now)** under **Can be spent on** (the default, **Any purchase**, is the everyday voucher described above).
2. The voucher is taxed at the gift voucher item's own tax code — give that item the item's rate in the catalogue (e.g. a *Coffee voucher* item with the 7% code).
3. Fill in the card number, value and holder as usual and **Add**. The basket line says *Specific item (taxed now)*. The sale records the VAT at that rate straight away, so the voucher shows in the day's VAT bands like a normal sale line — not in the **GUTSCHEINE** liability section.

Redeeming one:

1. In the **Split** tab, choose **voucher** as the method and type the code, then **Check balance**.
2. For a specific-item voucher the till does not fill in an amount — it shows the voucher's value and holder next to a **Redeem** button instead. Such a voucher can never be used as a payment: it has already been taxed, so taking it as payment would tax the same money twice.
3. Hand over the item and tap **Redeem**. This is final: the voucher is marked as used at once, with nothing to complete or remove afterwards. No further payment or tax is recorded — the sale that sold the voucher already did that.

What can go wrong:

- A gift voucher item with no tax code is taxed at the shop's standard rate — set the right tax code on the item first.
- Tapping **Redeem** on a voucher that has already been used, or scanning a specific-item voucher at the sale screen, is refused with a message saying so.
- A specific-item voucher sold on another till can be checked from this one but not yet redeemed here — redeem it on the main till or on the till that sold it.

## Day-end reports and other tills

The day-end (Z) report has its own **GUTSCHEINE** section for vouchers sold and spent that day, and voiding a sale that sold a voucher cancels the voucher with it (only while it's unused) — see [Reports & end of day](/help/reports). A voucher sold on one till can be spent on any other till in the shop; how that works while a till is offline is covered in [Multiple tills (one shop)](/help/multitill).
