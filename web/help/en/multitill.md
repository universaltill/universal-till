---
id: multitill
title: Multiple tills (one shop)
section: Connecting & extending
order: 310
summary: "Run several tills on the same shop network: one is the main till, the others join it and share the catalog, prices, settings and stock automatically."
routes: [/tills, /ui/tills/pending-pairings]
keywords: [second till, pairing, sync, lan, primary]
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
