---
id: vouchers
title: Gift vouchers
section: Everyday selling
order: 25
public: true
summary: Sell gift vouchers by tapping a Gift voucher item — scan the card or let the till create a number — and take them as payment by scanning them at the sale screen or in the payment panel's Split tab.
keywords: [voucher, gift voucher, gift card, gutschein, hediye çeki, einzweck, single-purpose, specific item, redeem, balance, issue, code, card number, item type, list, history, export, csv, correct, correction, adjust, adjustment, top up, top-up, reload, add value]
routes: [/vouchers, /vouchers/{id}]
---

# Gift vouchers

A gift voucher is money a customer pays now to spend later — on anything in the shop, at any till. The till keeps each voucher's balance under its **card number**: the one printed on a physical card you hand over, or a 13-digit number the till creates when you leave the number empty. You sell a voucher by tapping a **Gift voucher** item on the sale screen, and take one as payment by scanning it or in the payment panel's **Split** tab (see [Payments](/help/payments) for the tab itself).

## Selling a voucher

Selling needs an item whose **Type** is *Gift voucher* in the catalogue (see [Catalog](/help/catalog)). A new shop already has one, ready to sell: it is named in the shop's language when that language is installed during setup, otherwise *Gift voucher* until that language is installed, and then it is renamed into it, unless you have renamed it yourself (rename it in the catalogue if you like). If a catalogue import then brings its own gift voucher item, the one created at setup is removed, as long as nothing has used it yet, and the import summary says so. On a till that was set up before this, create one once. Leave its price empty to be asked for the value every time, or give it a price (e.g. *Gift voucher €25*) to start the popup with that value.

1. Tap the gift voucher item on the sale screen (or scan its barcode). The **Gift voucher** popup opens.
2. **Card number**: scan the card you're handing over — the field is ready for your scanner as soon as the popup opens — or type its number. Leave it empty and the till creates a number when you tap **Add**.
3. **Value**: the voucher's value, a normal currency amount, e.g. `25.00`. It starts with the item's price when the item has one.
4. **For** is optional — a name to remember who the voucher was bought for.
5. **Can be spent on**: **Any purchase** is the everyday voucher; **Specific item (taxed now)** is described below. Tap **Add**.
6. The voucher joins the basket as its own line, showing its card number. Take the payment as usual (**Payment**, cash, card, …). The receipt lists every voucher under **Vouchers issued** with its number, who it is for, and the number as a barcode, on the printed receipt and on screen, so scanning the receipt later redeems it like a card. For a number the till created, this receipt is where the customer finds it, so hand it over or write the number on the card. Turn on **Print each gift voucher on its own slip** (see [Receipts & printing](/help/printing)) to also print each voucher on its own slip, ready to hand over. Where the receipt shows its subtotal and tax lines, a **Vouchers issued** row in the totals (just above the total) carries the face value of the vouchers sold, so the rows add up to the total.

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

## The Gift vouchers page

**Menu → Gift vouchers** (manager only) lists every voucher this till knows about, newest first: its card number, who it is for, its original value, what is left on it, its status — **Active**, **Used up** or **Void** — and what it can be spent on (a specific-item voucher is marked). **Sold in** links to the receipt of the sale that sold it; an imported balance says so instead.

1. Type in the **Search** box to find a voucher by card number or holder — the list updates as you type. Use **Status** to show only active, used-up or void vouchers. Search ignores upper and lower case, accented letters included (**MÜLLER** finds *Müller*). The search and status stay in the page address, so going Back from a voucher, or reloading, keeps them.
2. Tap a card number to see the voucher's **History**: when it was issued, every time it was redeemed and every correction, with the amount and a link to each sale's receipt.
3. **Export CSV** downloads every voucher matching the current search and status — all of them, not just the ones on screen — with the columns `code`, `balance`, `label`, `original_amount`, `status`, `purpose` and `created_at`. The file can be imported on another till with **Import voucher balances** at the bottom of the same page (see [Import voucher balances](/help/voucher-import)).

Good to know:

- The page shows the newest 200 vouchers. When there are more it says so — search or filter to find older ones.
- With no vouchers yet, the page tells you how to start: sell one with a Gift voucher item, or import balances from your previous system.
- On an additional till, the list shows only the vouchers this till has seen — sold, imported or redeemed here. The full list is on the main till. To open a voucher sold elsewhere, search for its full card number: the till asks the main till for it.

## Correcting a balance

When a voucher's balance is wrong — the value was keyed wrong when it was sold, or a damaged card is replaced — a manager can set the right balance from the voucher's own page. No money changes hands: the till records the correction, who made it and why.

1. Open **Menu → Gift vouchers** and tap the voucher's card number.
2. Under **Correct balance**, check the **Current balance**, then type the **New balance** — the full amount the voucher should have from now on, not the difference.
3. Type a **Reason** (required, up to 200 characters), e.g. *amount keyed wrong when sold*.
4. Tap **Save correction**. The page reloads with **Balance corrected.**, the new balance, and an **Adjustment** line in the **History** showing the difference (+ or −), the reason and who made it.

A correction to 0 marks the voucher **Used up**; raising the balance of a used-up voucher makes it **Active** again. The voucher's original value never changes. The day-end (Z) report lists the day's corrections on their own **Adjusted** line in **GUTSCHEINE** — never as sales, payments or VAT.

What can go wrong:

- Only a manager or admin can correct a balance.
- A voided voucher, or a voucher for a specific item, can't be corrected — the page says why instead of showing the form.
- "The new balance is the same as the current balance" — there is nothing to correct.
- "This voucher's balance changed while you were looking at it" — someone spent it, or corrected it, in the meantime. Reload the page, check the new balance, and correct again if it's still needed.
- On an additional till, the correction is made on the main till. If the main till can't be reached, nothing is changed and the page says so — try again when it's back. If the page says the main till didn't confirm the correction, it may or may not have been saved: reload the page and check the voucher's history before you try again.

## Topping up a voucher

A customer can add value to a voucher they already have — the card keeps its number and its balance goes up. A top-up is a sale, just like selling a new voucher: the customer pays for it at the till, and nothing is taxed until the voucher is spent.

1. Open **Menu → Gift vouchers** and tap the voucher's card number.
2. Under **Top up**, type the **Amount to add**, e.g. `20.00`, and tap **Add to sale**.
3. The sale screen opens with a gift voucher line for this card number, marked *Top-up*. Add anything else the customer is buying, then take the payment as usual.
4. When the sale is paid, the voucher's balance goes up by the amount. The receipt lists it under **Vouchers issued** as a *Gift voucher top-up*, with the card number, the amount added and the new balance, and the voucher's **History** shows a **Top-up** line linked to the receipt. With **Print each gift voucher on its own slip** on, a top-up also prints on its own slip with the amount added and the new balance — see [Receipts & printing](/help/printing).

A used-up voucher becomes **Active** again; its original value never changes. Nothing is added to the voucher until the sale is paid — tap ✕ on the line to drop the top-up. The day-end (Z) report counts a top-up with the vouchers sold in **GUTSCHEINE**. Voiding the sale takes the amount back off the voucher, but only while the customer hasn't spent it.

What can go wrong:

- Only a manager or admin can top up a voucher.
- A voided voucher, or a voucher for a specific item, can't be topped up — the page says why instead of showing the form.
- The amount must be above zero, and a voucher's balance can't go above the till's limit for one voucher.
- A voucher can be topped up once per sale, and it can't pay for its own top-up.
- A top-up needs an active gift voucher item in the catalogue — if there is none, the page says so.
- Voiding the sale is refused once part of the topped-up amount has been spent.
- On an additional till, the main till must be reachable (and up to date) when you add the top-up; if it isn't, the page says so and nothing is added to the sale.
- On an additional till, a sale that sold or topped up a voucher can't be voided: the till says so and nothing changes, and the voucher keeps its value.

## Printing a voucher's slip again

A customer who lost the gift slip they were handed can have it printed again, without printing the sale's receipt. A manager or admin does this from the voucher's page.

1. Open **Menu → Gift vouchers** and tap the voucher's card number.
2. Tap **Print voucher slip**. The slip comes out on this till's receipt printer, and the page shows "Slip sent to the printer." With no receipt printer set up on this till, the browser's print dialog opens with just the slip instead; choose a printer there (or save it as a PDF). The page then shows "Print dialog opened for this slip." Receipts and gift voucher slips printed through the browser's print dialog are set up to leave out the web address, page title and date. If your browser still adds them, turn off *Headers and footers* (under *More settings* in Chrome and Edge) in the print dialog.

The button shows for an **Active** voucher with a balance, whether or not a receipt printer is set up and whether or not **Print each gift voucher on its own slip** is on. The slip carries the shop name and the receipt's header lines (and logo, if your receipt shows one), **Gift voucher** and its value, **For …** when a name was entered, the barcode (on a Regular printer the number prints as text instead), and a line **Reprinted** with today's date, so it can be told from the slip handed over at the sale. No receipt prints and the cash drawer doesn't open. A slip printed through the browser has no logo and prints black on white.

The slip shows the voucher's **original value**, not what is left on it. When the two differ, the page says so above the button — check the current balance on the voucher's page before you hand the slip over.

What can go wrong:

- Only a manager or admin can print a slip.
- With no receipt printer set up, the slip opens in the browser's print dialog, and the page also links to **Receipt printer** in Settings. The audit log records it as opened in the print dialog, because the till can't tell whether it was printed or the dialog was cancelled.
- The print dialog can only print on a printer the device itself knows about. On a till with no printer at all (for example a kiosk set up from the till image) it can only save a PDF, and in the Android app it opens Android's print dialog (a printer needs an Android print service, or choose Save as PDF). Set up a receipt printer there instead.
- If **Print failed** appears, see "Nothing is printing" in [Receipts & printing](/help/printing).
- A voided or used-up voucher has no button; if it was voided or used up after you opened the page, the till says so and prints nothing.
- On an additional till the slip prints on that till's own printer. It decides on the main till's status when the main till is reachable, and on this till's own record when it isn't.
- Every slip printed or opened in the print dialog this way is recorded in the audit log.

## Day-end reports and other tills

The day-end (Z) report has its own **GUTSCHEINE** section for vouchers sold, spent and corrected that day, and voiding a sale that sold a voucher cancels the voucher with it (only while it's unused, and not on an additional till, which refuses that void) — see [Reports & end of day](/help/reports). A voucher sold on one till can be spent on any other till in the shop; how that works while a till is offline is covered in [Multiple tills (one shop)](/help/multitill).
