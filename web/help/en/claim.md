---
id: claim
title: Store registration & claiming
section: Connecting & extending
order: 320
summary: Registering connects your till to the Universal Till marketplace; claiming links the store to YOUR account so you get the online back-office (My stores, fleet view) and paid features.
keywords: [register, claim, cloud, marketplace, store, subscription, plan, pair, pairing code, re-pair]
---

# Store registration & claiming

Registering connects your till to the Universal Till marketplace; claiming links the store to YOUR account so you get the online back-office (My stores, fleet view) and paid features.

## How to use it

1. Settings → Till registration shows the store identity; Register now connects it if needed. On a joined till, the main till registers it for you: this card then says it is registered through the main till, and claiming is done on the main till.
2. Tick the box "I accept the Universal Till cloud service terms (version v0.1-draft)" before registering; "Read the terms" opens them in a new tab. Without the tick, Register now does nothing and tells you so. You accept once per terms version: afterwards the card shows "Terms accepted: version v0.1-draft" with the date instead of the box, and the till sends your acceptance (the version and your language) with the registration. A till that was registered before this step existed (or through a plugin install) shows the same box under Till registration with an **Accept terms** button: tick the box and press it. If the till is offline, your acceptance is kept on the till and the card says it has not reached the cloud yet; press **Send to the cloud** once you are back online. Selling is never affected.
3. Click Claim this store to get a short code (valid 15 minutes) and a QR — scan it with your phone to claim from there.
4. Sign in to the marketplace with your Universal Till ID, open the claim page and enter the code.

## Automatic registration — your choice at setup

The setup wizard's last screen asks once whether to register the till right away — not ticked unless you tick it (a till that joined a shop with a pairing code is not asked: pairing already registered it). The same choice lives in Settings → Till registration as "Register this till with the marketplace automatically":

1. Turn it on and the till sends its device ID, store name, shop country, region and software version to the Universal Till cloud marketplace (used for support, updates and licensing) and registers straight away. Your address is not sent. Offline just then? Nothing is held up and setup still finishes — but the till stays unregistered until you open the plugin store or press Register now, so check Settings → Till registration once you are back online. The terms box appears under the choice while it is ticked; if you tick the choice but not the terms, the choice is saved as off and nothing is sent. In Settings, turning it on needs the same tick (unless you already accepted or the till is already registered); turning it off never does.
2. Turn it off and the till goes back to registering only when you first use the plugin store or press Register now. Turning it off never removes a registration that already happened.

## Subscription

Settings → Subscription (managers only) shows your plan as this till last heard it from the Universal Till cloud, the date it is paid until and when it was last confirmed. Selling, receipts and reports on the till are free on every plan and never stop because of the subscription.

- Local (free): nothing to do, the card just says you are on the free plan.
- Active: your plan, the date it is paid until and the last confirmation.
- Subscription not confirmed: the till couldn't confirm your plan with the cloud for more than 7 days, so paid features (such as cloud sync, managing products from a browser and managed TSE set-up) are paused. Check the till's internet connection; this clears by itself once it connects.
- Subscription ended: your subscription is no longer active, so paid features are paused until you renew it in your Universal Till account. For 90 days after the plan was last paid, a till whose subscription has ended checks with the cloud by itself once an hour, so it usually picks up a renewal within the hour. To see it straight away, press Check now on this card or on the paused-features notice in Settings → Till registration (managers and admins only): the till asks the cloud and shows the answer in place, either the date the plan is now paid until or that it is still not renewed. On a till registered through the main till, press it on the main till: this till gets the renewal from there.

In the last two cases a chip in the status bar opens the panel described below, and Till registration and Receipt signing (TSE) say in place which of their features are paused. Nothing is hidden, and a TSE that is already set up keeps signing receipts.

**Renew or change plan** (managers and admins only) is on this card, on the paused-features notice in Till registration and on the status-bar chip. It opens a panel over the screen you are on. The till first asks the cloud for your current plan: if it was already renewed (for example in your Universal Till account), the panel shows the date it is paid until straight away, with Change plan if you want a different one. Otherwise choose Renew current plan or Choose a plan and scan the QR code with your phone: you pay on your phone, never on the till. While the code is shown the till checks every 20 seconds, for up to 10 minutes, and switches to your new plan as soon as it arrives. Close (or Escape) takes you back to exactly where you were. A till registered through the main till renews from the main till.

## Cloud sync is part of the paid plans

Registering and claiming are free, and so is the plugin store. Keeping the till in sync with your online account (sales figures, the product list, changes made on the shop's cloud manage page) is part of the paid plans. While the shop is not on one, the till does not sync with the cloud, and Settings → Till registration says that cloud sync is off. It only checks in briefly when it is installed, after an update, and when it is first started each day.

1. Once the shop is on a paid plan, the till finds out when it is next started on a new day, or sooner when someone on it registers it, pairs it, installs a plugin or generates a claim code. You can also press **Check for a paid plan** in Settings → Till registration (a manager or admin approves it). Cloud sync then starts within a minute and keeps running by itself.
2. If the plan ends, cloud sync stops after the till's next check-in. Selling never depends on it: every sale works offline either way.

## When the cloud stops accepting this till

If the cloud refuses this till's credential three times in a row, a chip appears in the status bar. It never blocks a sale: the till keeps selling offline, tries the cloud again once an hour, and keeps everything it still has to send on the till until it is connected again. The chip says one of three things:

- **Removed from the shop's cloud account**: the shop's owner removed this till from the store online.
- **This till needs pairing again**: the till's cloud credential no longer works, so the till must be paired with the shop again.
- **Register as a new store**: the store has no owner account, so the till can't be paired. Register it again as a new store; only the old fleet history is lost.

A manager can tap the chip to open Settings → Till registration, straight to Pair with a shop below. The chip goes away after the next successful contact with the cloud.

## When Register now or Claim this store fails

The card says what went wrong in one line; selling is never affected:

- **Accept the Universal Till cloud service terms to register this till**: the terms box was not ticked, so nothing was sent. Tick it (or turn on automatic registration with the box ticked) and try again.
- **Tick the box to accept the Universal Till cloud service terms**: on an already registered till, Accept terms was pressed without ticking the box, so nothing was recorded or sent. Tick it and press Accept terms again.
- **Couldn't reach the cloud. Your acceptance is saved on this till**: the acceptance is kept on the till; check the internet connection and press Send to the cloud again.
- **The Universal Till cloud is not available for this shop**: the cloud does not serve this shop. On a joined till this comes from the main till. The till keeps working offline.
- **No cloud address is set up on this till**: the till was installed without a cloud address, so it can't register. Ask whoever installed it to set one up.
- **Another registration attempt is still running**: the till is already registering in the background. Wait a moment and try again.
- **Name your shop first**: the shop has no real name yet (it is blank or a default such as "My Store"), so the till has not registered it with the cloud. Follow the **Shop name** link in the message, save the shop's name, then press Register now again.
- **Registration failed** or **Could not get a claim code**: the till could not reach the cloud. Check the internet connection and try again.

## Pair with a shop (and re-pairing)

Use this when the shop already exists in the Universal Till cloud and this till should join it, or when a till was removed from the shop's cloud account or says it needs pairing again.

1. In the shop's cloud account, choose "Add or re-pair a till" to get a pairing code (8 characters, valid 15 minutes, single use).
2. On this till, open Settings → Till registration → Pair with a shop, enter the code and press Pair. A manager or admin approves it.
3. The till replaces only its own cloud connection: it takes a new device ID and a credential of its own. Sales, products and settings stay on the till, and selling keeps working offline the whole time.
4. On a joined till, the code must come from the same shop as its main till; a code for another shop is refused and nothing changes. A refused or expired code leaves the till unregistered until you pair it with a fresh code.

## The shop's currency

A shop created in the cloud account (my.) takes its currency from its country. When the main till of such a shop has not completed or voided a sale yet, it switches to the shop's currency by itself at its next cloud sync (waiting while a sale is in progress or parked), and the switch is recorded in the audit log. Joined tills follow the main till.

Once a till has a sale, it never changes its currency by itself: that would relabel every amount already recorded. It keeps selling in its own currency, a manager sees a **Currency differs from shop** chip in the status bar (hold the pointer on it to see both currencies), and the cloud account lists the same problem for this till. Tap the chip to open Settings → Currency and change it there once that is safe for your records. The chip goes away as soon as the two match. A currency this till version doesn't know is never switched to; it is shown the same way until the till is updated.
