---
id: payments
title: Payments
section: Everyday selling
order: 20
summary: Cash is built in; card and other payment methods come as plugins from the store — for example Stripe with a card reader (terminal) or QR-code payment.
keywords: [cash, card, tender, change, split]
---

# Payments

Cash is built in; card and other payment methods come as plugins from the store — for example Stripe with a card reader (terminal) or QR-code payment.

## How to use it

1. Install a payment plugin from the Plugin store and enter your account keys in its settings.
2. If you use a card reader, set its id in the plugin settings on that till.
3. At checkout, the preferred method leads and each button can show an estimated fee (set provider fees in Settings → Payments) — pick the cheaper one; a declined card leaves the basket untouched.
4. These quick buttons under **Pay** always charge the *exact* amount owed in that one method — there's nowhere on them to record change or to split the total. For anything else (change due, more than one method), use **Split** — see below.

## Giving change on a cash sale

The quick Cash button has no way to record change, because it always tenders the exact total. To hand change back, switch to the **Split** tab instead, even for an otherwise ordinary single-method cash sale:

1. Choose **Cash** (or whichever method the customer handed over) and type the amount they actually gave you — in normal currency amounts, e.g. `5.00`, not the smallest unit.
2. Type how much change to give back in the **Change** box, then **Add Payment**. A pending-payment card appears showing what the sale actually collects (amount minus change), with a note of the change given right alongside it.
3. **Complete Sale**. The receipt records both the amount tendered and the change given back on that payment.

What can go wrong: a change amount bigger than what was tendered is refused before it's added — fix the amount or the change and try again. Leaving Change at `0` (or blank) is just a normal exact cash payment.

## Splitting a payment across methods

Use this when one sale is paid for with more than one method — part card, part cash, a gift-card plugin topping up the rest, and so on.

1. Open the **Split** tab under the basket.
2. For the first portion: pick its method, type its amount (and change, and an optional reference — a card reader's transaction id, for instance), then **Add Payment**. It's added to a running list of pending payments, each shown with its net amount and a ✕ to remove it if you added it in error.
3. Repeat for each further portion. **Fill Remaining** does the arithmetic for you — it fills the Amount box with whatever the pending payments don't yet cover, so you don't have to work out the last portion by hand.
4. Once the pending payments cover the total, **Complete Sale**. (If you only ever need one payment, typing it into the form and pressing Complete Sale straight away works too — it's added for you first.)
5. **Clear** empties the whole pending list and starts over.

What can go wrong:

- **Complete Sale** with nothing entered and nothing pending is refused with a prompt to add a payment first.
- **Complete Sale** with the pending payments still short of the total is refused with a message that the amount received doesn't cover the sale total — add the rest (or use **Fill Remaining**) before trying again.
- **Fill Remaining** itself is refused if the pending payments already cover the total (there's nothing left to fill), or if the basket isn't in a state to accept a payment right now.
- The Amount and Change boxes here take a normal currency amount (e.g. `2.50`) — unlike the small per-line discount box on the basket itself, which takes the smallest currency unit (see **Selling & checkout**'s Discounts section) — so don't carry that habit over between the two.
