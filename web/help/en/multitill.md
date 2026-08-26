---
id: multitill
title: Multiple tills (one shop)
section: Connecting & extending
order: 310
summary: "Run several tills on the same shop network: one is the main till, the others join it and share the catalog, prices, settings and stock automatically."
routes: [/tills, /ui/tills/pending-pairings, /registers, /sync-quarantine]
# /registers and /sync-quarantine are never screenshotted (docs-shots only
# captures routes[0]) — accepted gap, see e2e/tests-docs/lib.js's
# routedTopics() comment (ut-docs#900).
keywords: [second till, pairing, sync, lan, primary, register, quarantine]
---

# Multiple tills (one shop)

Run several tills on the same shop network: one is the main till, the others join it and share the catalog, prices, settings and stock automatically.

## How to use it

1. On the **main till**, go to Settings → Tills and show the pairing code. It is a block of text (or a QR code) that contains both the address and a one-time token.
2. On the **second device**, install the till app. On its first setup screen choose **Join an existing shop**. There are two ways to join: find the main till on the network automatically, or paste the pairing code by hand.
3. **Find it automatically** (easiest): press **Find a primary on this network**, pick the main till from the list, give this till a name and press **Request to pair**. Both screens now show the same 6-digit code — check they match, then approve the request on the main till's Tills page (a manager does this). The request expires after 10 minutes if nobody approves it. **Or paste the code**: paste the **whole** pairing code from step 1 into the Pairing code box, give the till a name, and press Join. The main till's address on its own is not enough — the code carries the one-time token that authorises the enrolment.
4. Wait for it to finish. It copies the whole shop across — catalog, prices, settings, stock and operators — so on a busy shop network this can take up to a minute. Do not press Join twice: the token is single-use, and a second press fails with "code used or expired".
5. If it fails, the reason appears under the button. The most common causes are the two tills not being on the same network, and only part of the code having been pasted.
6. Sales made on any till flow back to the main till, and catalog changes spread to all tills within about half a minute.
7. On a joined till, Catalog and Inventory are read-only — a banner at the top says so and points you to the main till. On Windows and Mac that banner is a clickable link; on the kiosk app it's plain text instead, since the kiosk has no way back if a link took it to another till's screen.
8. Stock is decided by the main till only. A joined till never refuses a sale for being out of stock — its own stock number is just a copy that can lag a few seconds behind, so it always lets the sale through. If that means an item's stock genuinely goes below zero (two tills selling the last one at almost the same moment), it shows up as a problem on the main till's dashboard rather than blocking anyone's sale.
9. On the **main till's** own Tills page, the Enrolled list also shows the main till itself (labelled "this till") alongside any joined tills, using the name set in Settings — so a single-till shop still sees its till listed, not just an empty table.
10. On a **joined till**, the Tills page shows the same shop-wide list too — the main till (labelled "the primary") and every other joined till, including itself (labelled "this till"). It's read-only there: only the main till can remove ("revoke") a joined till.
11. The small sync chip in the top-right of the nav shows this till's own name and is clickable on both the main till and a joined till, opening the Tills page (its sync state comes through as the chip's colour: green means caught up, amber means it hasn't been heard from recently). A single-till shop with nothing joined yet doesn't show a chip at all — there's nothing to sync yet.
   - On the **main till**, the chip also turns amber and shows a count of **quarantined** entries if a joined till ever sends a sale it can't apply (an extremely rare edge case — a voucher redeemed twice on two tills at once, for example). Rather than being silently lost, that one sale is held for review on the main till's **Settings → Sync quarantine** page, which lists the till, receipt number, reason and time. This never blocks any other sale from syncing normally.
12. Plugins follow the main till too: install or remove a plugin on the **main till** and every joined till applies the same change automatically within about half a minute. Each till downloads the plugin from the plugin store itself and verifies it before it runs — plugins are never copied from till to till. Trying to install, remove, enable, disable or update a plugin directly on a joined till is refused with a message pointing you to the main till. The one exception is a plugin imported from a file: importing still works on any till, including a joined one, but the plugin stays only on that till and does not spread.

## Registers

A **register** is a till/checkout station — the thing a shift is opened on and a sale is rung up against. A shop can have more than one: a front counter and a back counter, for instance, each taking sales at the same time.

- Open **Registers** from the menu (manager only) to create one. Give it a name and, optionally, the stock location it draws from; a manager can rename it, change its stock location, or deactivate it later (deactivating keeps its sales/shift history — it just stops offering it for a new shift). Changing the stock location doesn't touch its past sales or shifts — those stay tied to the register itself. A register that already has shift/sale history shows a "Has shift/sale history" hint on this page — informational only, it never blocks deactivation.
- Joining a second till (see above) assigns it a register **automatically**: enrolment creates a new register on the main till, named after the joining till (if that name is already taken, a numeric suffix is added, e.g. "Till 2 (2)"), and the joined till arrives already set to it — no manual step, and it can open its own shift straight away. The manual path still exists when you want more: create further registers from **Registers** (a shop can have more registers than tills), rename one there, or change which register a till is on that till under **Settings → Tills** → **This till's register**.
