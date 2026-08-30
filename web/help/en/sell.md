---
id: sell
title: Selling & checkout
section: Everyday selling
order: 10
summary: "The main register screen: scan or pick items, take payment, print or skip the receipt."
routes: [/, /ui/basket, /ui/buttons, /ui/held, /refund/{receipt}]
keywords: [basket, checkout, tender, pay, scan, hold, discount, camera, table, tables, modifier, customization, customer, loyalty, promo, order type, takeaway, dine in, suggestions, offline, sync chip]
---

# Selling & checkout

The main register screen: scan or pick items, take payment, print or skip the receipt. It works fully offline — a network outage never blocks a sale.

## How to use it

1. Scan a barcode, or find and tap the item on the sell screen: switch between category tabs, or type into the search box to filter the current tab by name, then tap the tile to add it.
2. No barcode scanner? On a device with a camera and a supported browser, tap the 🔳 button next to the scan box to scan with the camera instead — point it at the barcode or QR code; the item rings up automatically and the camera closes. This never replaces or disables a plugged-in scanner, and no photo or video ever leaves the device.
3. Adjust quantity or remove lines in the basket, then tap **Payment** — this opens a panel over the item buttons (the basket stays visible on the left) where you pick Cash, Card, or another method, or switch to the Split tab to take more than one payment against the same sale. Close the panel (✕) to go back and adjust the basket without paying.
4. Hold a basket to serve the next customer and recall it later; refunds are under the sale history.
5. If you've set up tables (see **Tables & floor plan**), tap the **Table** button next to the dine-in/takeaway switch to open the table picker, then pick a table — or "No table" to clear it — and the dialog closes on its own. A held order keeps its table, shown as a chip on the held-orders strip; tap **Move table** on the strip to move a held order onto a different free table without losing it or resuming it.
6. To refund a completed sale, open it from Journal → sale history: pick which lines and how much of each to return (already-refunded quantity is tracked, so you can't return more than was sold), then choose cash or the original payment method to refund into. If the original sale carried a service charge, your share of it comes back automatically, in proportion to how much of the sale you're refunding — refund everything and the whole service charge comes back too.

## Item customization

Some items are set up in the catalog with customization groups — a coffee's size, a sandwich's bread, a burger's extras — each with its own price. See **Catalog, variants & barcodes** for setting those groups up; this section is about ringing one up.

Tap a customized item's tile and a picker opens instead of the item going straight into the basket. Pick an option in each group — a group that only allows one choice shows round buttons, a group that allows more than one shows checkboxes — then choose **Add to cart**. Options that cost extra show their added price right next to the choice; the basket line then shows every option you picked underneath the item's name, with the total already folded into the line price.

A group can be required (marked with a **\***) or optional, and can have its own minimum and maximum number of choices — most items just need one pick per group, but nothing stops a shop setting up a group that wants two of four. If you pick too few or too many for a group, **Add to cart** doesn't go through: a message names the group and how many choices it needs. **Cancel** closes the picker without adding anything.

Once a customized line is in the basket, its options are locked in for that sale — there's no way to edit them in place. To change a customer's mind, remove the line (✕) and tap the item again to reopen the picker.

## Discounts

There are two separate kinds of discount, and they don't stack the same way — a per-line discount only affects that one basket line, a sale-level discount affects the whole basket total.

**A per-line discount** comes off one basket line. Each line has a small discount box just below its quantity box — type the amount to take off in your currency's smallest unit (e.g. type `30` to take 30p, or 30 cents, off that line) and move off the box; the basket updates immediately, the line's price drops, and the box keeps showing that same smallest-unit amount (e.g. `30`). Clear the box back to `0` to remove the discount. It can't go negative, and it only ever reduces that one line.

**A sale-level discount** — a coupon or promotion code set up under **Promotions & promo codes** — applies to the whole sale at once. Scan the code, or type it into the barcode box and choose **Add**, exactly like ringing up an item: the till recognizes it isn't an item barcode, checks it's an active promotion, and applies it across the whole basket instead of one line. A success message names the code, and a new **Discount** line appears in the basket's totals (a percentage code shows the percentage next to it, e.g. "Discount (10.00%)"). See **Promotions & promo codes** for creating and managing codes, including codes tied to one specific customer.

What can go wrong: the scan box doesn't tell a bad promo code apart from an unrecognized item barcode — an inactive, expired, or unknown code is refused with the same "Item not found" message a mystery barcode gets, so a code that never scanned probably means it isn't active (or the shop's clock has moved past its end date) rather than a scanner problem. A code restricted to a specific customer only applies once that customer is linked to the sale (see below) — scanned before that, it's refused the same way.

A per-line discount that's bigger than the line itself is accepted while you're building the basket, but the sale is refused when you try to take payment, with a generic "couldn't be completed" message that doesn't say why — if a payment is unexpectedly refused, check that every line's discount is smaller than that line's own price.

## Dine-in or takeaway

A switch above the basket flips the current sale between **Dine in** and **Takeaway** — tap it to change which one is active; its label and color show which is currently selected. This matters for tax: some items' tax rate can differ between eating in and taking away, and the shown total updates immediately when you switch, using whichever is selected at that moment. It resets to Dine in (the default) any time the basket is reset for the next customer.

## Linking a customer to a sale

If you keep a customer list, scanning or typing their code into the barcode box links them to the current sale instead of adding an item — the same box you scan products into. This only recognizes a **customer code** in the shape the till expects (starting `CUST` or `LOY`) — a phone number or any other loyalty number typed in on its own isn't recognized as a customer lookup and is refused as "Item not found" like an unmatched product barcode; issue customers a code in the recognized shape (see **Users, PINs & shifts** or your customer-management plugin's own setup) if you want to look them up this way. A recognized-but-unknown code gets its own message instead ("Customer not found"). Once linked, a success message names the customer and their name shows above the basket totals for the rest of the sale.

Linking a customer is what lets a customer-specific promotion code (see **Discounts** above) be recognized — scan the customer first, then their code.

## "Customers also buy" suggestions

A row of suggestion chips can appear under the basket totals, based on what tends to sell alongside what's already in the basket — worked out entirely from this shop's own past sales, with nothing sent over the network. Tap a chip to add that item straight to the basket. Unlike tapping the item's own tile, this always adds it directly — for an item that's normally customized (see **Item customization** above), the chip skips the customization picker rather than opening it, so double-check a customized item added this way.

The strip shows nothing at all — not even an empty box — whenever there's nothing to suggest: an empty basket, a basket of items with no strong sales pattern together, or if the lookup itself fails for any reason. It never blocks or interrupts a sale; treat it as a hint, not a step you need to act on.

## Status chips in the side menu

Several small chips near the bottom of the left-hand menu tell you, at a glance, what's going on with the till — none of them block the sale screen:

- Your own name (👤) is who's currently signed in; tapping it opens Change PIN, and the **Lock** button next to it signs out to the PIN pad. See **Users, PINs & shifts** for that and for the manager-only shortcuts that show up next to it (Users, Promotions, Translations).
- If this till syncs with others, a chip shows this till's name and its sync state (caught up, or behind/offline with a queued count) — see **Multiple tills (one shop)** for exactly what it shows. A single till with nothing joined shows no sync chip at all.
- If your shop has a TSE configured, a fiscal signing chip shows its health — see **German shops: TSE and real sales** below.
- A bug-report chip is also there if that feature is enabled — see **Reporting a bug**.

## Selling with no network connection

The till is built to keep selling when the internet, the shop's own network, or another till it syncs with is unreachable — a network outage never blocks checkout. What actually changes on screen:

- **Nothing about ringing up items, customizing them, applying discounts, or holding a basket needs the network** — all of that runs against this till's own local data.
- If a request genuinely can't reach the till's own server (the browser tab itself lost connectivity), a banner reading "Connection problem — the till keeps working offline." appears at the top of the sale screen; it clears itself the next time a request succeeds, so it never has to be dismissed by hand.
- If this till syncs with others (see **Multiple tills (one shop)**), the sync status chip in the left-hand menu turns amber and marks itself offline/queued rather than disappearing — sales keep recording locally and catch the other tills up once the connection returns.
- Payment still works the same way: cash and any offline-capable payment method complete normally. A payment method that itself needs the network (a card reader calling out to its processor, for instance) is a property of that specific method, not of the till. There's an offline checkbox next to the Pay buttons for marking a sale offline up front — note that it's a standing setting, not a one-off: once ticked it stays on (even after closing the till) until you untick it again, so switch it back off once the outage is over.
- Printing a receipt is a local connection to the printer (USB, LAN, or Bluetooth), not an internet one, so it's unaffected by an internet outage on its own; a genuinely unreachable printer is reported separately (see **Receipts & printing**).
- A held basket, once resumed, completes exactly the same way — nothing about holding or recalling a sale depends on connectivity either.

What can go wrong: none of this pauses or blocks a sale by itself. The one thing that *can* pause selling is the separate Germany TSE fiscal-signing check described below — and that check explicitly does not treat being offline as a fault.

## German shops: TSE and real sales

If your shop's country is Germany and an administrator has put the shop into system-of-record mode (recording real, legally-binding sales, not trial/demo), the till checks the shop's TSE (technical security device) before completing each sale, **refund, or cash payout** — a refund and a payout each move real money the same way a sale does, so all three go through the identical check, not separate ones. "Cash payout" here means anything that takes cash out of the drawer on the Shifts page: a payout or a cash adjustment that removes cash, and a bottle-deposit (Pfandrückgabe) payout. Adding cash to the drawer (a float top-up) is not affected.

- **No TSE set up:** the sale, refund or payout is refused with a message on-screen. There is no override for this state. To fix it, set up a TSE — your own hardware, your own cloud account, or a managed subscription — or ask an administrator to take the shop out of system-of-record mode until it's ready.
- **TSE set up but currently failing:** sales, refunds and cash payouts are paused. An owner (admin) can grant a temporary override — it needs a typed confirmation phrase, a reason, and a duration (up to 8 hours). While the override is active a banner stays on the sale and refund screens, every sale, refund or payout taken is flagged in the audit trail, and the receipt notes that the TSE was unavailable. When the time runs out, they pause again automatically.
- Trial and demo shops (not in system-of-record mode) are never affected by this check.

This check only looks at your shop's own settings on the till — it never depends on the network, and being offline is not treated as a TSE fault.

### When TSE signing can't be reached mid-sale

If a fiscal signing plugin is installed, the till asks it to sign each sale at the moment of payment (this takes up to a few seconds when the signing service is slow). When the signing service can't be reached — or the till already knows it's offline — the sale still completes normally: checkout is never blocked by the network. Instead, the gap is recorded openly, permanently:

- the sale is flagged as unsigned in the audit trail,
- the customer receipt carries a notice that the sale was recorded without a TSE signature,
- and a warning appears in the till's problems list.

This is permanent, not a pending recovery: the till does not retry signing a sale after it has completed. TSE providers (fiskaly's own guidance for the German SIGN DE service, among others) do not permit signing a transaction after the fact, so a sale that fails to sign at the moment of payment stays on the till's own records as unsigned — the notice on the receipt and in the audit trail is the final word on that sale, not a placeholder waiting to be replaced. A signing outage never pauses selling by itself — whether the cause is the network or the signing service, the till keeps completing sales and recording the gap exactly as described above. (The system-of-record pause described earlier on this page is a separate safeguard; a signing outage does not trigger it.)

### No signing plugin installed at all

The two situations above assume a signing plugin exists but couldn't be reached, or refused this one sale. A shop can also end up with a TSE configured and system-of-record mode on, yet **no fiscal-signing plugin installed at all** — nothing subscribed to sign anything, ever. Sales still complete (the same proceed-and-declare behaviour as an outage, above), but this is the kind of gap that's easy to miss sale by sale, so **Settings** carries a persistent notice for it — "No fiscal signer installed" — for as long as the condition holds, with a link straight to the Plugins page to install the country's tax plugin. It isn't dismissable: it checks the current state every time Settings is opened, and disappears on its own the moment a signing plugin is active.

### No tax-rate plugin installed

Separately from signing, some countries (today: Germany) need a plugin to apply the correct dine-in/takeaway VAT rate to each sale. This can happen even when signing is working fine, or when no TSE is configured at all. If your shop's country needs this and no working tax-rate plugin is currently active, **Settings** carries a persistent notice — "No tax-rate plugin installed" — with a link straight to the Plugins page. Like the fiscal signer notice above, it isn't dismissable: it checks the current state every time Settings is opened, and disappears on its own the moment a working tax-rate plugin is active. What happens to sales in the meantime depends on why the plugin isn't answering: if none is installed at all, sales still complete, just at a single flat rate until you install one; if a plugin is installed but broken, the till refuses to complete sales that need it, the same fail-closed behaviour described for a broken signing plugin.

### The fiscal signing status chip

If your shop has a TSE configured, a small chip near the bottom of the left-hand menu shows fiscal signing's current health at a glance: **✓ Fiscal signing OK**, or **⚠ Fiscal signing unavailable** if the most recent sale on this till completed without a TSE signature, together with how many sales today are still unsigned. It shows nothing at all when no TSE is configured — no chip, no warning, for a shop that will never need one. This is a status indicator only, sourced entirely from the till's own record of what it has signed — it never blocks the sale screen, and it never depends on reaching the network.

### When a sale can't be signed at all

Separately from an outage, a signing plugin can say that one specific sale can't be signed as it stands — for example a tip or a discount that its signing service can't reconcile into a valid receipt record. This is different from the service being unreachable: it's a property of that one sale. The sale still completes normally, and the same things happen as with an outage — it's flagged in the audit trail and a warning appears in the till's problems list — but the customer receipt notice reads differently: it says the sale could not be signed as presented, not that TSE signing was unavailable, so it never suggests a connectivity problem that didn't happen. As with an outage, this is permanent — the till does not attempt to sign the sale again later.

### The TSE signature block on receipts

When the signing plugin signs a sale and returns the signing details, the receipt includes a "TSE signature" block showing the signing details the plugin returned: the TSE serial number, the transaction number, the signature counter, the transaction start and end times, the signing algorithm, and the signature itself. Both the on-screen receipt and the thermal print also render these details as a scannable QR code alongside the text lines (the QR format is provisional and will be confirmed against a certified TSE before the German rollout). The block only appears when the signing service actually returned this data for that sale — a receipt without it means the data was not provided, and nothing is ever filled in with placeholder values.
