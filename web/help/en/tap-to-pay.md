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

1. Open **Settings → Payments** and tap **Tap to Pay on iPhone**. The page shows four steps and a status at the top: **Ready**, **Getting ready**, **Needs setup** or **Not available**. The next step to do and its button are shown at the top, under the status.
2. **This iPhone** is checked for you. It needs an iPhone XS or newer with up-to-date software.
3. Tap **Allow card taps** and confirm. This lets the Stripe payment method start a card tap on this iPhone and talk to Stripe's payment service. Only a manager or admin can do it, and the Stripe payment method must be turned on in **Plugins** first; if it isn't, the page says so under the button. The change is recorded with your name.
4. Tap **Set up**. The button stays off, with a line telling you why, until card taps are allowed. Apple asks you to accept its terms for Tap to Pay on iPhone and may ask for location access. Follow the steps on the iPhone screen; it can take a minute.
5. **Stripe account** is read only here. If it says to add your key, do that in the shop's online account, then come back to this page.
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
- **Phone card readers belong to the main till.** A spare phone is paired to the main till as a card reader; an additional till can send a sale to it through the main till (see [Card readers](/help/card-readers)).
- **Set up an older additional till again if asked.** A till set up a long time ago may say it needs to be set up again before it can take Tap to Pay. Ask the shop owner to set it up again; nothing was charged and other payments still work.

## What can go wrong

- **This device is an iPad, or the iPhone is too old** — Tap to Pay on iPhone needs an iPhone XS or newer. Pair an iPhone as a card reader instead (see [Card readers](/help/card-readers)).
- **This till can't take Tap to Pay** — only the main till and additional tills can. A satellite is joined to another till: use Tap to Pay on the main or an additional till.
- **Card taps can't be refunded here** — you are on an additional till. Refund the sale on the main till.
- **This till needs to be set up again** — ask the shop owner to set it up again, then open this page.
- **Set up didn't finish** — read the reason under it and tap **Set up** again. Under **Details for support** the page shows the exact code; send it to support if the same thing keeps happening.
- **This iPhone isn't linked to the shop's online account yet** — link it to the shop's online account, then come back and tap **Set up**.
- **This app is connected to a test server** — Tap to Pay can't work until the app uses the shop's online account. Send support the code under **Details for support**.
- **This version of the app can't take Tap to Pay** — install the app from the App Store.
- **This iPhone is busy with a payment or another setup** — wait for it to finish, then try again.
- **It was cancelled on the iPhone screen** — tap **Set up** to start again.
- **Apple or Stripe reported a problem** — try again. If it keeps happening, send support the code under **Details for support**.
- **The payment service isn't answering right now** — try again in a few minutes. The page never treats this as "not set up".
- **Card payments aren't switched on for this shop yet** — switch them on in the shop's online account.
- **No Stripe account is connected to this shop** — connect one in the shop's online account.
- **Can't reach the payment service** — there is no internet connection. Check it and open the page again. The page never treats this as "not set up".

See [Payments](/help/payments) for how a cashier pays with a card.
