---
id: claim
title: Store registration & claiming
section: Connecting & extending
order: 320
public: false
summary: Registering connects your till to the Universal Till marketplace; claiming links the store to YOUR account so you get the online back-office (My stores, fleet view).
keywords: [register, claim, cloud, marketplace, store, subscription, plan, pair, pairing code, re-pair]
routes: [/settings/cloud/move/{id}]
---

# Store registration & claiming

Registering connects your till to the Universal Till marketplace; claiming links the store to YOUR account so you get the online back-office (My stores, fleet view).

## How to use it

1. Settings → Till registration shows the store identity. There is no Register button: once setup is finished the till registers itself in the background (see below). On a joined till, the main till registers it for you: this card then says it is registered through the main till, and claiming is done on the main till.
2. The Universal Till Merchant Terms (version merchant-terms-v1.0) are accepted when you claim the store, when you use a paid feature and when you download a plugin (the plugin page shows a box to tick). A till that is already registered shows the box under Till registration with an **Accept terms** button: tick the box ("I accept the Universal Till Merchant Terms (version merchant-terms-v1.0)"; "Read the terms" opens them in a new tab) and press it. You accept once per terms version: afterwards the card shows "Terms accepted: version v0.1-draft" with the date instead of the box. If the till is offline, your acceptance is kept on the till and the card says it has not reached the cloud yet; press **Send to the cloud** once you are back online. Selling is never affected.
3. Click Claim this store to get a short code (valid 15 minutes).
4. Sign in to your Universal Till account on another device and enter the code there.
5. Once the store is claimed, the card says "This store is linked to your Universal Till ID." and the Claim button is gone (with a link to My stores). If this till was removed from the shop or must be paired again, open **Reconnect this till to a shop** on the same card and enter a pairing code: it replaces only this till's cloud connection; its sales, products and settings stay. Until the store is claimed (or while the till has not yet heard either way), the Claim button and the open Pair form stay as they were.

<!-- plan-purchase -->

Alongside the code, the till shows a QR code and a link to the claim page: scan the QR with your phone, or open the link, and claim from there. This is the only way to claim on a shell that cannot open a browser.

<!-- /plan-purchase -->

The Google Play and App Store versions of the app show the code as text only: type it in on another device.

## Automatic registration and the Privacy card

Every till registers itself once setup is finished, with no question to answer and no button to press. It does this in the background, never during a sale, and keeps trying (waiting longer between tries, up to ten minutes) until the cloud answers; a till that is offline at the end of setup carries on selling and registers when it is back online. The setup wizard's last screen, and Settings → **Privacy**, say exactly what the till tells Universal Till:

- Each time the till starts: its software version, release channel (how this copy was installed), system type, country, language, the shop name you chose, a random till code and when it last started. Your address is not sent.
- When the till updates itself, or a plugin is installed, updated or removed: which version or plugin changed, and when. The till keeps these in a short queue and sends them with its next check-in; it never sends anything when it is switched off.
- Never your sales, customers, staff or items.

A till set up before this existed shows the same notice once, in Settings → Privacy; a link in the status bar points there, and the till registers only after the notice was shown. A manager can switch off **Send usage facts** there: the till then registers nothing and reports nothing about itself (cloud features you use still connect), and the card says **Paused** until you switch it on again. Stopping does not delete what the cloud already holds; ask us to delete it. The card also shows whether the till is registered and when the cloud last answered it.

## Subscription

Settings → Subscription (managers only) shows your plan as this till last heard it from the Universal Till cloud, the date it is paid until and when it was last confirmed. Selling, receipts and reports on the till are free on every plan and never stop because of the subscription.

- Local (free): nothing to do, the card just says you are on the free plan.
- Active: your plan, the date it is paid until and the last confirmation.
- Subscription not confirmed: the till couldn't confirm your plan with the cloud for more than 7 days, so paid features (such as cloud sync, managing products from a browser and managed TSE set-up) are paused. Check the till's internet connection; this clears by itself once it connects.
- Subscription ended: your subscription is no longer active, so paid features are paused until it is active again. For 90 days after the plan was last paid, a till whose subscription has ended checks with the cloud by itself once an hour, so it usually picks up a renewal within the hour. To see it straight away, press Check now on this card or on the paused-features notice in Settings → Till registration (managers and admins only): the till asks the cloud and shows the answer in place, either the date the plan is now paid until or that it is still not active. On a till registered through the main till, press it on the main till: this till gets the renewal from there.

In the last two cases a chip appears in the status bar, and Till registration and Receipt signing (TSE) say in place which of their features are paused. Nothing is hidden, and a TSE that is already set up keeps signing receipts.

<!-- plan-purchase -->

**Renew or change plan** (managers and admins only) is on this card, on the paused-features notice in Till registration and on the status-bar chip. It opens a panel over the screen you are on. The till first asks the cloud for your current plan: if it was already renewed (for example in your Universal Till account), the panel shows the date it is paid until straight away, with Change plan if you want a different one. Otherwise choose Renew current plan or Choose a plan and scan the QR code with your phone: you pay on your phone, never on the till. While the code is shown the till checks every 20 seconds, for up to 10 minutes, and switches to your new plan as soon as it arrives. Close (or Escape) takes you back to exactly where you were. A till registered through the main till renews from the main till.

<!-- /plan-purchase -->

## Cloud sync is part of the paid plans

Registering and claiming are free, and so is the plugin store. Keeping the till in sync with your online account (sales figures, the product list, changes made on my.universaltill.com) is part of the paid plans. While the shop is not on one, the till does not sync with the cloud, and Settings → Till registration says that cloud sync is off. It only checks in briefly when it is installed, after an update, and when it is first started each day.

1. Once the shop is on a paid plan, the till finds out when it is next started on a new day, or sooner when someone on it registers it, pairs it, installs a plugin or generates a claim code. You can also press **Check for a paid plan** in Settings → Till registration (a manager or admin approves it). Cloud sync then starts within a minute and keeps running by itself.
2. If the plan ends, cloud sync stops after the till's next check-in. Selling never depends on it: every sale works offline either way.

## When did this till last sync? (Sync now)

On a till with cloud sync, the status bar at the bottom of the screen shows when the till last heard from the cloud: "Synced just now", "Synced 5 min ago", "Synced at 14:05", or "Not synced yet". After 10 minutes without an answer it turns amber. Selling carries on either way, and every sale works offline.

1. Changes made on my.universaltill.com reach the till at its next sync. On some plans they arrive within seconds; otherwise within about 2 minutes.
2. Not willing to wait? A manager or admin presses **Sync now** in the status bar. The till asks the cloud straight away and the time updates within a few seconds. If the till is offline, it says "Still trying — sales keep working offline" and syncs once the connection is back.
3. On a phone, open ☰: the status row is at the bottom of the menu.
4. On an iPhone the app syncs only while it is open. Open it to pick up changes.
5. An additional till does not show this. It gets the shop's changes from the main till (see the main-till light in its status bar).

## When the cloud stops accepting this till

If the cloud refuses this till's credential three times in a row, or this till's data was restored onto a new device, a chip appears in the status bar. It never blocks a sale: the till keeps selling offline, tries the cloud again once an hour, and keeps everything it still has to send on the till until it is connected again. The chip says one of these things:

- **Removed from the shop's cloud account**: the shop's owner removed this till from the store online.
- **This till needs pairing again**: the till's cloud credential no longer works, so the till must be paired with the shop again.
- **Cloud sign-in lost**: the store has no owner account, so the till can't be paired. Tap the chip, then **Reconnect this till** in Settings → Till registration (same shop); only if that is refused, **Register as a new shop** (only the old fleet history is lost).
- **Restored on a new device — connect again**: this till's data was restored from a phone or iCloud backup (or copied) onto a new device, so its cloud connection was reset; the sales on the till are not touched. Pair it with the shop again, or register it as a new store, in Settings → Till registration. The old device keeps its own connection. A restored till never registers itself as a new shop; you choose what happens.
- **Cloud reconnect paused** (with "in use on two machines" in Settings → Till registration): the same till data is running on two machines (a copied folder), so the cloud will not connect it again. Keep the data on one machine; the till retries once a day.

A manager can tap the chip to open Settings → Till registration, straight to Pair with a shop below. The chip goes away after the next successful contact with the cloud. The restored chip goes away once the till is paired or registered again.

## When Claim this store or Accept terms fails

The card says what went wrong in one line; selling is never affected:

- **Accept the Universal Till Merchant Terms to register this till**: the terms box on the plugin's page was not ticked, so nothing was sent. Tick it and try again.
- **Tick the box to accept the Universal Till Merchant Terms**: on an already registered till, Accept terms was pressed without ticking the box, so nothing was recorded or sent. Tick it and press Accept terms again.
- **Couldn't reach the cloud. Your acceptance is saved on this till**: the acceptance is kept on the till; check the internet connection and press Send to the cloud again.
- **The Universal Till cloud is not available for this shop**: the cloud does not serve this shop. On a joined till this comes from the main till. The till keeps working offline.
- **No cloud address is set up on this till**: the till was installed without a cloud address, so it can't register. Ask whoever installed it to set one up.
- **Another registration attempt is still running**: the till is already registering in the background. Wait a moment and try again.
- **Registration failed** or **Could not get a claim code**: the till could not reach the cloud. Check the internet connection and try again.

## Pair with a shop (and re-pairing)

Use this when the shop already exists in the Universal Till cloud and this till should join it, or when a till was removed from the shop's cloud account or says it needs pairing again.

1. In the shop's cloud account, choose "Pair a till" (or "Pair again" for a till that is already listed) to get a pairing code (8 characters, valid 15 minutes, single use).
2. On this till, open Settings → Till registration → Pair with a shop, enter the code and press Pair. If this till is already connected to a shop, it first asks you to confirm that you want to replace that connection and shows the code you typed, so you can check it; Cancel changes nothing. A manager or admin approves it afterwards.
3. The till replaces only its own cloud connection: it takes a new device ID and a credential of its own. Sales, products and settings stay on the till, and selling keeps working offline the whole time.
4. On a joined till, the code must come from the same shop as its main till; a code for another shop is refused and nothing changes. A refused or expired code leaves the till unregistered until you pair it with a fresh code. On a till that is connected to a shop, a code that is wrong for the shop also leaves it disconnected until you pair it with a fresh code (this is why the till asks first); a joined till is registered again through its main till.

## Moving this till into another shop

Where the Universal Till cloud offers it, pairing a till that already belongs to a shop with a code from another shop you own **moves** the till into that shop instead of adding it as a second till. The till keeps its identity, and its sales history, products and settings go with it: the two shops become one. Nothing moves until a manager or admin approves it on the till itself.

1. Enter the code under Pair with a shop and press Pair. The till asks you to confirm the move, then shows the move page. The till stays in its shop and keeps selling while the move waits.
2. On the move page, check the shop it moves into and the time the request expires (about a day). To approve, type that shop's name and press **Approve move**, then enter a manager's or admin's PIN. **Decline** also asks for a PIN. A cashier can't open the request or act on it.
3. Until someone decides, managers and admins see a banner on every page except the sale screen: "*shop* asked to move this till into their shop". **Review** opens the move page. On every page, the sale screen included, everyone (cashiers too) sees a **Till move waiting for a manager** chip in the status bar. It names no shop and changes nothing; for a manager or admin, tapping it opens the move page. It goes away once the move is decided, cancelled or expired.
4. After approval the till says "Moved into *shop*"; the cloud account shows the merged history within a few minutes. Selling never stops.

Good to know:

- Approving sends all of this shop's sales history to the other account. Support never asks you to do this; decline a move you did not ask for.
- The till must be online to approve or decline. Offline, the buttons are off and nothing is sent later.
- Three wrong PINs decline the move. If the owner cancels it in the cloud account, or nobody decides in time, the banner says so and nothing changes.
- A move is refused, with nothing changed, when this till's shop has other tills, a fiscal device, a paid plan or another owner, when the two shops use different currencies or keep their data in different regions, or when the code was not made by the other shop's owner. The message says which.
- "This till's approval key changed" means the till cannot sign moves: contact support. A till whose old shop no longer recognises it and that has sales is not paired as a new till (its sales would be left behind); contact support to merge it.

## When the cloud knows this till but will not sign it in

A shop with no owner account has no pairing code, so the cloud decides by the till's **approval key** (a key the till keeps in its own data folder, never in a backup). Selling is never affected. What you see depends on the state:

- **Cloud sign-in lost** (status bar): the till's cloud sign-in was refused three times. A manager opens Settings → Till registration. The device ID and the **key fingerprint** (32 characters in groups of four) are shown there, always, to managers only. Tap **Reconnect this till**: the till proves its key and gets a new sign-in in the same shop. If that is refused, the card says why (never the cloud's raw text).
- **The cloud knows a different key for this till**: the key on this till is not the one the cloud holds (for example after restoring the data onto new hardware, or a damaged key file). Ask support to allow it. Read them the device ID and the key fingerprint from Settings; support matches both against the shop's fleet row (shop name, till name, version) and lets in one reconnect for 24 hours, once, only for a till holding that key. If this till is still registered (its cloud sign-in was lost) and they cannot match it, or you would rather not wait, use **Register as a new shop**. A till that is not registered yet does not offer Register as a new shop; ask support.
- **This till was in use recently** (**Cloud reconnect paused**): for a till on an older version that never reported a key, the cloud refuses a re-registration while the till was in use in the last 72 hours. This is a weaker check: an unpaid till often contacts the cloud rarely, so it can be past 72 hours while still selling. The till tries again by itself after the date shown. Support can allow one reconnect sooner, the same way.
- **The cloud could not do this right now**: nothing changed; try again later.
- **This till's software is too old to prove its identity**: update this till, then try again.
- **Too many tries**: wait a few minutes.

**Register as a new shop** (below Reconnect, with a confirmation and a manager's or admin's PIN) leaves the old shop behind in the cloud with this till's earlier cloud history, gives the till a new device ID and registers it as a new shop. Sales on the till stay on the till. It cannot be undone. If the new registration fails, the till is simply not registered yet and registers itself again in the background. A till whose database was wiped already has a new device ID and registers as a new shop by itself. A shop that has an owner re-pairs the till instead (Pair with a shop).

## The shop's currency

A shop created in the cloud account (my.) takes its currency from its country. When the main till of such a shop has not completed or voided a sale yet, it switches to the shop's currency by itself at its next cloud sync (waiting while a sale is in progress or parked), and the switch is recorded in the audit log. Joined tills follow the main till.

Once a till has a sale, it never changes its currency by itself: that would relabel every amount already recorded. It keeps selling in its own currency, a manager sees a **Currency differs from shop** chip in the status bar (hold the pointer on it to see both currencies), and the cloud account lists the same problem for this till. Tap the chip to open Settings → Currency and change it there once that is safe for your records. The chip goes away as soon as the two match. A currency this till version doesn't know is never switched to; it is shown the same way until the till is updated.
