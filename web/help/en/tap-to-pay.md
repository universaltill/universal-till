---
id: tap-to-pay
title: Tap to Pay on iPhone
section: Everyday selling
order: 22
public: true
summary: Get this iPhone ready to take card payments by tapping a card or phone on it, and know what the cashier sees after a tap.
keywords: [tap to pay, iphone, contactless, card, stripe, apple, terms, allow card taps, declined, pin]
routes: [/settings/tap-to-pay, /settings/tap-to-pay/status]
---

# Tap to Pay on iPhone

Tap to Pay on iPhone lets this iPhone take a contactless card or phone payment with nothing but the iPhone. Only the shop's main till can take it, and only a manager or admin can set it up.

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

## What can go wrong

- **This device is an iPad, or the iPhone is too old** — Tap to Pay on iPhone needs an iPhone XS or newer. Pair an iPhone as a card reader instead (see [Card readers](/help/card-readers)).
- **Only the main till can take Tap to Pay** — this till is joined to another. Set it up on the main till.
- **Set up didn't finish** — read the reason under it (terms, location, no internet, Apple did not answer in time) and tap **Set up** again.
- **Can't reach the payment service** — check the internet connection and open the page again. The page never treats this as "not set up".

See [Payments](/help/payments) for how a cashier pays with a card.
