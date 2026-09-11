---
id: printing
title: Receipts & printing
section: Running the business
order: 230
summary: Prints receipts, invoices and end-of-day reports on a thermal receipt printer or a regular office printer; kitchen orders can print to a separate printer; also covers shelf/price labels and what to check when nothing prints.
# No routes: of its own. The printer setup fields live inside Settings
# (/settings), a page already claimed by display.md's routes: — the
# settings.html card has its own {{ helpLink "printing" }} hint instead of
# a competing routes: claim. Reprinting a receipt happens from a page
# already claimed by reports.md (/journal/{receipt}), and the kitchen/
# receipt print-failure warnings live on a page already claimed by
# order-status.md (/orders). This is a related but distinct accepted gap
# from ut-docs#900's /locations /registers case (see the comment in
# web/help/en/inventory.md): #900 is a topic whose OWN route is never
# screenshotted; this topic legitimately claims NO route at all, because
# every surface it documents already belongs to another topic's page.
keywords: [printer, receipt, kitchen, labels, paper, reprint, nothing printing, test print]
---

# Receipts & printing

Prints receipts, invoices and end-of-day reports on a thermal receipt printer or a regular office printer; kitchen orders can print to a separate printer.

## Setting up your printer

1. In Settings choose your printer's **Connection**: **Network (IP)** for a WiFi/Ethernet thermal printer, **USB device** for one plugged in (e.g. `/dev/usb/lp0` on Linux/Pi), or **Regular printer (system / HP, plain text)** to send receipts as plain text to your computer's own default printer instead — put a specific printer name in Address to target one, or leave it blank for the default.
2. Don't know your network printer's address? Use **Find printers on this network** to scan for it instead of typing it in by hand — a manager-only button next to the printer address fields. It only finds printers that advertise themselves as AppSocket/JetDirect (the common type for network receipt printers); an IPP-only printer, or one connected by USB, won't show up and still needs its address or device path entered manually.
3. Use **Print test receipt** to check the connection — it prints a short "TEST PRINT" slip on whichever printer is currently configured. It only shows for managers and admins; a cashier viewing this page doesn't see the button at all.
4. A kitchen printer can be set separately so food orders print where they are prepared — the same **Find printers on this network** button offers a candidate for either field. There's no separate test button for it: **Print test receipt** only ever checks the receipt printer above, never the kitchen one. To confirm a kitchen printer genuinely works, ring up an order that reaches it and check the ticket actually comes out.
5. Need more than one kitchen printer — a grill printer and a bar printer, say? See **Kitchen stations** to route categories or individual items to their own station.
6. Kitchen tickets print the order type and station header in the till's configured language, rather than always in English. There's no separate kitchen-only language setting — it follows the till's one configured locale.
7. Cash drawer not opening after a cash sale? Most drawers are wired to pin 2 (the default) — if yours needs pin 5, set it under **Cash drawer pin** in the printer settings.
8. Currency symbols printing garbled — `€` or `£` coming out as strange characters? Many thermal printers don't understand UTF-8. Since v0.12.12 the till picks this for you: a shop configured in euros or pounds, with a Western European language, sends **Western Europe (CP858 — €/£)** automatically, and a till that was already set up is switched over once, the first time it starts on this version. A shop in euros or pounds with a Croatian, Slovenian or Slovak language sends **Central Europe (Windows-1250)** the same way (pounds only switches when the currency symbol is actually covered — Windows-1250 has no `£`, so a Croatian/Slovenian/Slovak shop in pounds stays on UTF-8); Estonian, Latvian or Lithuanian sends **Baltic (Windows-1257)**; Greek sends **Greek (Windows-1253)**; and Turkish sends **Turkish (Windows-1254)** — Baltic, Greek and Turkish cover both `€` and `£`. You only need **Characters** in Settings → Printer if you want to override that — for example a printer that genuinely does handle UTF-8. This only changes what is sent to the receipt printer — the till's language support on screen is unaffected. A shop billing in Turkish lira (`₺`) stays on **UTF-8**: none of the code pages above can print the lira sign, so switching wouldn't fix the garbling. Arabic and Farsi tills likewise stay on **UTF-8** and are never switched automatically — no single-byte code page here covers those alphabets.
9. **Receipt after each sale** (Settings → Printer) decides what happens to the paper receipt when a sale completes: **Always print** (the default — every sale prints), **Ask the customer** (the receipt screen shows *Would you like a receipt?* with **Print receipt** / **No receipt** — the sale is already complete either way, and the usual Print button stays available below), or **Never print automatically** (the cashier prints from the receipt screen when needed). Shops in Germany are fixed to Always print for now, while the receipt rules for German shops are being confirmed — an installed country plugin can likewise limit the choices to what its market permits.

## Reprinting a receipt

Any receipt can be printed again later — a customer without theirs, a faded slip, or a second copy for a delivery driver. Open the sale from the Journal and use its **Print receipt** button; see "Reconciling a card payment (receipt detail)" in [Reports & end of day](/help/reports) for how to find a past sale there. If the reprint fails, it just shows a plain **"Print failed"** next to the button, without the specific reason — the underlying cause is still one of the ones in "Nothing is printing" below, so use **Settings → Printer**'s own test print to see which one.

## Printing shelf/price labels

Print a barcode label for a shelf or a price ticket straight from an item's own entry — no separate label-design step.

1. Open **Catalog**, open the item, and go to its **Print labels** tab (the item must already be saved once — a brand-new, unsaved item shows a hint instead of the form).
2. Choose **Whole item** or one specific **Variant** — a variant label carries that variant's own price and barcode (e.g. "Apples Large" scans as the large apples, not the item's base code), so pick the exact size/flavour you're labelling.
3. Set **Copies** (1–50; a blank or invalid number is treated as 1, and anything typed over 50 is capped at 50) and press **Print labels**.
4. An item or variant with no barcode and no SKU has nothing to print — the button reports **"This item has no barcode or SKU to print"** instead of sending a blank label; give it one on the Variants tab first (see [Catalog, variants & barcodes](/help/catalog)).
5. Any operator can print labels — it's normal floor work, not a manager action.

Labels go to whichever printer is set up above (the receipt printer, not the kitchen one) — the same "Nothing is printing" causes below apply here too, e.g. the button reports a plain **"Print failed"** if no printer is configured yet. **Labels cannot print at all on a Regular printer (system) connection** — the button just reports "Print failed" with no further explanation, because label printing only understands a real receipt printer (Network or USB device); this is a known gap, not something you're doing wrong.

## Kitchen tickets

A completed sale automatically sends a kitchen ticket the moment the sale finishes — there's nothing to press. If the kitchen printer (or a station's printer) is unreachable, the order still completes, but that order shows a **⚠ Kitchen print failed** warning on the Orders board so it's never silently lost. There is currently no button to resend just that ticket, and — unlike the receipt warning — nothing in the till clears the kitchen warning either: fix the printer, then tell the kitchen about the order yourself; the ⚠ stays on that order's card as a record that it happened. See [Order status (kitchen progress)](/help/order-status) for how the matching **⚠ Receipt print failed** warning works (that one does clear, by reprinting the receipt from the Journal).

## Nothing is printing

Work through these in order — they cover the printer failures Settings → Printer's own **Print test receipt** button reports in detail. A few other buttons (a label print, a Journal reprint, the receipt designer's own test print) share these same underlying causes but only ever show a bare "Print failed" — if one of those fails, run **Print test receipt** in Settings → Printer to see which of the causes below it actually is.

1. **No printer configured yet.** Settings → Printer's Connection is still **Off**. Pressing Print test receipt reports "Print failed: printer is off — pick a printer type first" — pick Network, USB device or Regular printer above and save first.
2. **A network printer's address is wrong, or the printer is unreachable** (powered off, on a different WiFi network, wrong IP). Print test receipt tries for a few seconds, then reports something like `Print failed: printer connect 192.168.1.50:9100: dial tcp 192.168.1.50:9100: i/o timeout` (or "connection refused" if something answers on that address but isn't listening for prints) — the address it names is exactly what's typed in Printer address, so check that first, then that the printer is actually on and on the same network. **Find printers on this network** (above) is the fastest way to confirm the right address.
3. **A USB device path is wrong, or the printer isn't plugged in.** Device mode reports the exact path it tried, e.g. `Print failed: printer open /dev/usb/lp0: open /dev/usb/lp0: no such file or directory` — check the printer is plugged in and powered on, and that the path matches what the operating system actually calls it (this can change if it's unplugged and replugged into a different port). A path that exists but the till isn't allowed to write to reports a permissions error the same way.
4. **Regular printer (system) mode has nothing set up to print to.** This mode hands the receipt to your computer's own printing system — if that system has no default printer, or the named one isn't there, the message names what the operating system itself said went wrong, the same way as above. (Shelf/price labels don't work at all in this mode — see "Printing shelf/price labels" above.)
5. **The printer is out of paper.** The till has no way to tell — a successful "Test receipt sent — check the printer" message, or a completed sale's silent print, only confirms the bytes were sent; the till can't see the printer's paper sensor. If everything above checks out but nothing physically comes out, check the paper itself before anything else.
6. **A kitchen order failed but the receipt printer is fine, or vice versa.** They're two separate printers with two separate warnings — a **⚠ Kitchen print failed** or **⚠ Receipt print failed** tag on the Orders board tells you which one to look at; fixing one never automatically retries the other. See "Kitchen tickets" above and [Order status (kitchen progress)](/help/order-status).
7. **Receipts aren't printing on their own, but Print test receipt works.** Check **Receipt after each sale** above — it may be set to **Ask the customer** or **Never print automatically** rather than Always print.
