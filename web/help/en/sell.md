---
id: sell
title: Selling & checkout
section: Everyday selling
order: 10
summary: "The main register screen: scan or pick items, take payment, print or skip the receipt."
routes: [/, /ui/basket, /ui/buttons, /ui/held, /refund/{receipt}]
keywords: [basket, checkout, tender, pay, scan, hold, discount, camera]
---

# Selling & checkout

The main register screen: scan or pick items, take payment, print or skip the receipt. It works fully offline — a network outage never blocks a sale.

## How to use it

1. Scan a barcode, or find and tap the item on the sell screen: switch between category tabs, or type into the search box to filter the current tab by name, then tap the tile to add it.
2. No barcode scanner? On a device with a camera and a supported browser, tap the 🔳 button next to the scan box to scan with the camera instead — point it at the barcode or QR code; the item rings up automatically and the camera closes. This never replaces or disables a plugged-in scanner, and no photo or video ever leaves the device.
3. Adjust quantity or remove lines in the basket, then choose Pay.
4. Hold a basket to serve the next customer and recall it later; refunds are under the sale history.
5. To refund a completed sale, open it from Journal → sale history: pick which lines and how much of each to return (already-refunded quantity is tracked, so you can't return more than was sold), then choose cash or the original payment method to refund into.

## German shops: TSE and real sales

If your shop's country is Germany and an administrator has put the shop into system-of-record mode (recording real, legally-binding sales, not trial/demo), the till checks the shop's TSE (technical security device) before completing each sale:

- **No TSE set up:** the sale is refused with a message on the sale screen. There is no override for this state. To fix it, set up a TSE — your own hardware, your own cloud account, or a managed subscription — or ask an administrator to take the shop out of system-of-record mode until it's ready.
- **TSE set up but currently failing:** sales are paused. An owner (admin) can grant a temporary override — it needs a typed confirmation phrase, a reason, and a duration (up to 8 hours). While the override is active a banner stays on the sale screen, every sale taken is flagged in the audit trail, and the receipt notes that the TSE was unavailable. When the time runs out, sales pause again automatically.
- Trial and demo shops (not in system-of-record mode) are never affected by this check.

This check only looks at your shop's own settings on the till — it never depends on the network, and being offline is not treated as a TSE fault.
