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

### When TSE signing can't be reached mid-sale

If a fiscal signing plugin is installed, the till asks it to sign each sale at the moment of payment (this takes up to a few seconds when the signing service is slow). When the signing service can't be reached — or the till already knows it's offline — the sale still completes normally: checkout is never blocked by the network. Instead, the gap is recorded openly, permanently:

- the sale is flagged as unsigned in the audit trail,
- the customer receipt carries a notice that the sale was recorded without a TSE signature,
- and a warning appears in the till's problems list.

This is permanent, not a pending recovery: the till does not retry signing a sale after it has completed. TSE providers (fiskaly's own guidance for the German SIGN DE service, among others) do not permit signing a transaction after the fact, so a sale that fails to sign at the moment of payment stays on the till's own records as unsigned — the notice on the receipt and in the audit trail is the final word on that sale, not a placeholder waiting to be replaced. A signing outage never pauses selling by itself — whether the cause is the network or the signing service, the till keeps completing sales and recording the gap exactly as described above. (The system-of-record pause described earlier on this page is a separate safeguard; a signing outage does not trigger it.)

### When a sale can't be signed at all

Separately from an outage, a signing plugin can say that one specific sale can't be signed as it stands — for example a tip or a discount that its signing service can't reconcile into a valid receipt record. This is different from the service being unreachable: it's a property of that one sale. The sale still completes normally, and the same things happen as with an outage — it's flagged in the audit trail and a warning appears in the till's problems list — but the customer receipt notice reads differently: it says the sale could not be signed as presented, not that TSE signing was unavailable, so it never suggests a connectivity problem that didn't happen. As with an outage, this is permanent — the till does not attempt to sign the sale again later.

### The TSE signature block on receipts

When the signing plugin signs a sale and returns the signing details, the receipt includes a "TSE signature" block showing the signing details the plugin returned: the TSE serial number, the transaction number, the signature counter, the transaction start and end times, the signing algorithm, and the signature itself. The on-screen receipt also renders these details as a QR code (the QR format is provisional and will be confirmed against a certified TSE before the German rollout); the thermal print shows them as text lines. The block only appears when the signing service actually returned this data for that sale — a receipt without it means the data was not provided, and nothing is ever filled in with placeholder values.
