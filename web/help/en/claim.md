---
id: claim
title: Store registration & claiming
section: Connecting & extending
order: 320
summary: Registering connects your till to the Universal Till marketplace; claiming links the store to YOUR account so you get the online back-office (My stores, fleet view) and paid features.
keywords: [register, claim, cloud, marketplace, store, pair, pairing code, re-pair]
---

# Store registration & claiming

Registering connects your till to the Universal Till marketplace; claiming links the store to YOUR account so you get the online back-office (My stores, fleet view) and paid features.

## How to use it

1. Settings → Till registration shows the store identity; Register now connects it if needed. On a joined till, the main till registers it for you: this card then says it is registered through the main till, and claiming is done on the main till.
2. Click Claim this store to get a short code (valid 15 minutes) and a QR — scan it with your phone to claim from there.
3. Sign in to the marketplace with your Universal Till ID, open the claim page and enter the code.

## Automatic registration — your choice at setup

The setup wizard's last screen asks once whether to register the till right away — not ticked unless you tick it. The same choice lives in Settings → Till registration as "Register this till with the marketplace automatically":

1. Turn it on and the till sends its device ID, store name, shop region and software version to the Universal Till cloud marketplace (used for support, updates and licensing) and registers straight away. Your address is not sent. Offline just then? Nothing is held up and setup still finishes — but the till stays unregistered until you open the plugin store or press Register now, so check Settings → Till registration once you are back online.
2. Turn it off and the till goes back to registering only when you first use the plugin store or press Register now. Turning it off never removes a registration that already happened.

## When the cloud stops accepting this till

If the cloud refuses this till's credential three times in a row, a chip appears in the status bar. It never blocks a sale: the till keeps selling offline, tries the cloud again once an hour, and keeps everything it still has to send on the till until it is connected again. The chip says one of three things:

- **Removed from the shop's cloud account**: the shop's owner removed this till from the store online.
- **This till needs pairing again**: the till's cloud credential no longer works, so the till must be paired with the shop again.
- **Register as a new store**: the store has no owner account, so the till can't be paired. Register it again as a new store; only the old fleet history is lost.

A manager can tap the chip to open Settings → Till registration, straight to Pair with a shop below. The chip goes away after the next successful contact with the cloud.

## Pair with a shop (and re-pairing)

Use this when the shop already exists in the Universal Till cloud and this till should join it, or when a till was removed from the shop's cloud account or says it needs pairing again.

1. In the shop's cloud account, choose "Add or re-pair a till" to get a pairing code (8 characters, valid 15 minutes, single use).
2. On this till, open Settings → Till registration → Pair with a shop, enter the code and press Pair. A manager or admin approves it.
3. The till replaces only its own cloud connection: it takes a new device ID and a credential of its own. Sales, products and settings stay on the till, and selling keeps working offline the whole time.
4. On a joined till, the code must come from the same shop as its main till; a code for another shop is refused and nothing changes. A refused or expired code leaves the till unregistered until you pair it with a fresh code.
