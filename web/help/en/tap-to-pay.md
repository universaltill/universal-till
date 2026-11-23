---
id: tap-to-pay
title: Tap to Pay on iPhone
section: Everyday selling
order: 22
public: true
summary: Get this iPhone ready to take card payments by tapping a card or phone on it, and know what the cashier sees after a tap.
keywords: [tap to pay, additional till, refund, iphone, contactless, card, stripe, apple, terms, allow card taps, declined, pin]
routes: [/settings/tap-to-pay, /settings/tap-to-pay/status]
---

# Tap to Pay on iPhone

Tap to Pay on iPhone lets this iPhone take a contactless card or phone payment with nothing but the iPhone. The shop's main till and its additional tills can take it, each on its own iPhone. Satellites and kiosks can't. Only a manager or admin can set it up.

## How to use it

1. Open **Settings → Payments** and tap **Tap to Pay on iPhone**. The page shows four steps and a status at the top: **Ready**, **Getting ready**, **Needs setup** or **Not available**. The next step to do is the only one with a button.
2. **This iPhone** is checked for you. It needs an iPhone XS or newer with up-to-date software.
3. Tap **Set up**. Apple asks you to accept its terms for Tap to Pay on iPhone and may ask for location access. Follow the steps on the iPhone screen; it can take a minute.
4. **Stripe account** is read only here. If it says to add your key, do that in the shop's online account, then come back to this page.
5. Tap **Allow card taps** and confirm. This lets the Stripe payment method start a card tap on this iPhone and talk to Stripe's payment service. The change is recorded with your name.
6. Tap **How to take a tap** to see Apple's short guide for staff.

When all four steps say **Done** the status shows **Ready**, and **Tap to Pay on iPhone** appears with the other ways to pay. Until then it is not offered to cashiers. A **Test mode** tag means the Stripe account is a test account and no real money moves.

## At the till

After the customer taps, the till shows one of these:

- **Approved**: the sale is recorded as paid by Tap to Pay on iPhone, with the card brand and last four digits.
- **Card declined. Nothing was charged.** Ask the customer to try another card or pay another way.
- **This card needs a PIN this iPhone can't take here. Nothing was charged.** Use another method, or try a different card.
- **Payment not confirmed**: the till could not learn the result. It may have been charged, so use **Check again** before taking any other payment.
- **Could not start the payment. Nothing was charged.** Try again or use another method. If it says Tap to Pay isn't set up yet, ask a manager to finish the steps above.

Stripe charges its own fee for each tap.

## On an additional till

An additional till takes card taps on its own iPhone, set up the same four steps as the main till.

- **It needs the internet to take a tap.** With no internet the till keeps selling: take cash or another payment instead. Tap to Pay on iPhone comes back by itself when the connection does.
- **Refunds are done on the main till.** To refund a card tap, open the sale on the main till and refund it there. An additional till will tell you so and will not send a refund.
- **Card readers stay on the main till.** Using a spare phone as a card reader is for the main till only (see [Card readers](/help/card-readers)).
- **Set up an older additional till again if asked.** A till set up a long time ago may say it needs to be set up again before it can take Tap to Pay. Ask the shop owner to set it up again; nothing was charged and other payments still work.

## What can go wrong

- **This device is an iPad, or the iPhone is too old** — Tap to Pay on iPhone needs an iPhone XS or newer. Pair an iPhone as a card reader instead (see [Card readers](/help/card-readers)).
- **This till can't take Tap to Pay** — only the main till and additional tills can. A satellite is joined to another till: use Tap to Pay on the main or an additional till.
- **Card taps can't be refunded here** — you are on an additional till. Refund the sale on the main till.
- **This till needs to be set up again** — ask the shop owner to set it up again, then open this page.
- **Set up didn't finish** — read the reason under it (terms, location, no internet, Apple did not answer in time) and tap **Set up** again.
- **Can't reach the payment service** — check the internet connection and open the page again. The page never treats this as "not set up".

See [Payments](/help/payments) for how a cashier pays with a card.
