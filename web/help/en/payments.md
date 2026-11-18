---
id: payments
title: Payments
section: Everyday selling
order: 20
public: true
summary: Cash is built in; card and other payment methods come as plugins from the store — for example Stripe with a card reader (terminal) or QR-code payment.
keywords: [cash, card, tender, change, split, preferred method, provider fee, refund, plugin, stripe, sumup, qr pay]
---

# Payments

Cash is built in; card and other payment methods come as plugins from the store — for example Stripe with a card reader (terminal) or QR-code payment.

## How to use it

1. Install a payment plugin from the store (Plugins → Store) and enter your account keys in its settings — Stripe with a card reader (terminal), SumUp, a QR-code payment provider, and others all install and configure the same way. A demo payment plugin is also available from the store for trying the payment panel out before a real provider is connected.
2. If you use a card reader, set its id in the plugin settings on that till.
3. At checkout, tap **Pay** (it shows the total) to open the payment panel (the basket stays visible while it's open) — the preferred method leads and each button can show an estimated fee (set the preferred method and its fees in **Configuring payment methods** below) — pick the cheaper one; a declined card leaves the basket untouched. The preferred method is the panel's first button and spans its whole width, so **Pay** then that button charges the exact amount owed in two taps.
4. On a card reader the customer has about 18 seconds to tap or insert. After that the till tells the reader to stop and shows **Payment declined**, so a card offered too late is refused. If the card was already being read when time ran out, the payment still goes through and the sale shows as paid. After a card decline that came after a wait, tap **Pay** again on the **same basket** first: the till checks the earlier attempt and, if it went through, completes the sale with it instead of charging again. If it is declined again straight away, the earlier attempt is still being checked — wait a minute and tap **Pay** again. If you change the basket or pick another method instead, check the reader's screen or your provider's dashboard before charging again.
5. These quick buttons under the panel's **Pay** tab always charge the *exact* amount owed in that one method — there's nowhere on them to record change or to split the total. For anything else (change due, more than one method), use **Split** — see below.

## Install, set up and update a card plugin (Stripe, SumUp)

Stripe and SumUp card payments are plugins from the Plugin Store. A manager installs them on the **main till** (**Plugins** → **Store**, open the plugin, accept the terms if asked, **Download**, then **Install**); joined tills receive them within about half a minute. The steps are the same in the Windows and Android store apps as in the direct download. Selling is never affected by any of this.

Then open the plugin's **Settings** on the **Plugins** page. Each field shows its name and a short help line (support may ask for a field's setting key, for example `sumup_api_key`; it shows when you point at the field name). Keys you paste there are secret: the till shows them hidden, and they stay on this shop's tills.

**Stripe**
- **Secret key** — your own key from the Stripe dashboard. A key starting `sk_test_` is test mode: nothing real is charged, and on a reader the till presents Stripe's simulated card for you. A key starting `sk_live_` charges real cards.
- **Currency** — leave the default unless you sell in another currency.
- **Card reader ID** — the id of your Stripe card reader (a WisePOS E or a Stripe Reader S700), set on each till that has one (this field is per till). Without a reader id the plugin makes a test-card demo charge that only works with a `sk_test_` key and never charges a real card; with a `sk_live_` key and no reader id every payment is declined. Set the reader id to take real cards.
- A wrong or revoked secret key declines every payment and refund; the till's log file names the reason `invalid_secret_key` (see [Where the till's log is](/help/recovery)). The cashier sees only the general **Payment declined** message, so check the key in the settings if every card is declined.

**SumUp**
- **API key** and **Merchant code** — your own, from your SumUp account.
- **Affiliate key** and **Affiliate app ID** — the application keys for card readers. Enter the affiliate key given to you; leave the app ID at its default unless you are told to change it. Without the affiliate key a reader payment is declined; the till's log file names the reason `no_affiliate_key`, and nothing is sent to SumUp.
- **Card payment currency** — **Automatic** uses your shop's currency.
- **Card reader ID** — the paired SumUp Solo reader for this till (this field is per till). With no reader id set, the plugin is in demo mode: it approves without charging a card (an amount ending in .13 is declined, .99 times out). Use it for training only and never for real sales.

A plugin added with **Import from file** starts with none of its permissions granted, and its payments are declined until a manager grants them on its settings page; a store install grants them for you.

Try it before real customers: install the plugin with a test key (Stripe) or in SumUp's sandbox, then take a small test payment and a test refund from the basket.

**Updating.** Payment plugins are not updated for you. A green **Plugin updates available** chip appears in the status bar when a newer version exists; open **Plugins**, update it on the main till, and try one small payment afterwards. If something goes wrong, **Versions** on the plugin's card rolls back to the earlier version (see [Plugins](/help/plugins)).

**Refunds.** A card refund goes back through the same provider. SumUp lists a refund as a separate entry in its dashboard, and the original payment still shows as paid there.

## Configuring payment methods

Once at least one payment method exists on the till (cash, card, gift card and voucher are there out of the box; a payment plugin adds its own branded method alongside them), a **Payments** card appears on **Settings**:

- **Preferred method** — which method leads at checkout: for cash, card and any payment-plugin method, it's the first, full-width button in the payment panel. (Picking a gift-card or voucher method here doesn't do this — those never appear as one-tap buttons in that panel, only under **Split**.) Pick it, then **Apply**. Leaving it at "—" lists every method in its normal order instead — cash first on a till with no payment plugin installed.
- **Provider fees** — what each provider charges you, as a percent plus a fixed amount per transaction (e.g. `2.50` % + `0.10`; a comma works too — `2,50` % + `0,10`), and the percent takes at most two decimals (`2.75` is fine, `2.875` is refused). The checkout panel uses these to show an estimated fee next to each method's button, so the cashier can steer a customer toward the cheaper one when it doesn't matter to them. Set a method's fee and **Save** it — a blank or zero fee is fine and just shows no estimate.

What can go wrong: a percent outside 0–100, or a negative fixed amount, is refused before it's saved — fix the number and try again.

Changing either setting needs a manager's or admin's role — the same [manager-approval prompt](/help/elevation) as other settings changes.

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

## When a card payment can't be confirmed

Sometimes a card reader can't tell the till whether a payment went through. The till then shows **The card may have been charged. Tap Pay again to settle it. Do not take another payment.** and keeps the sale on the screen.

- **Tap Pay again with the same payment.** The till asks the provider again, using the same request, so the customer is not charged twice. Any other payment (a different method or amount) is refused until this one is settled.
- **If you clear the sale instead** (**New sale**), park it, or open another order over it, the till sends back the card payments it already knows were taken for that sale, automatically. The payment it could not confirm is **not** sent back, because the customer may never have been charged. A red message tells you which case applies.
- **What to do next:** check the payment with your payment provider, and refund it there if the customer was charged. Do not charge the customer again until you have checked.
- **Where to find the details:** each payment (method, amount and request reference) is listed under **Recent problems** on the Back office page. Your provider's dashboard shows the final state of each payment.

The message appears once. If you miss it, **Recent problems** is the record. Nothing here needs the internet to start: the sale is cleared or parked straight away, and sending a payment back only waits on the card provider.

## Gift vouchers

To sell a gift voucher, or take one the till issued as payment, use the **Split** tab too — see [Gift vouchers](/help/vouchers).

## Refunding a payment

Refunds are started from the sale itself, not from this panel — open the original sale under Journal → sale history and choose **Refund**. The refund screen always offers cash, plus whichever other methods were actually used on that specific sale — so a split cash-and-card sale can be refunded into either, but the method list never grows an unrelated method the sale never used. See **[Selling & checkout](/help/sell)**'s refund step for the full walkthrough, including how a service charge is refunded proportionally. A card refund finds the original card payment from the sale itself, on any till, even if the card plugin was reinstalled since. If a card sale was taken before the till recorded the provider's reference for it, the refund is declined and you refund that card payment in your card provider's dashboard instead.
