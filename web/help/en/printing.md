---
id: printing
title: Receipts & printing
section: Running the business
order: 230
summary: Prints receipts, invoices and end-of-day reports on a thermal receipt printer or a regular office printer; kitchen orders can print to a separate printer.
keywords: [printer, receipt, kitchen, labels, paper]
---

# Receipts & printing

Prints receipts, invoices and end-of-day reports on a thermal receipt printer or a regular office printer; kitchen orders can print to a separate printer.

## How to use it

1. In Settings choose your printer and its type — thermal or regular.
2. Don't know your network printer's address? Use **Find printers on this network** to scan for it instead of typing it in by hand — a manager-only button next to the printer address fields. It only finds printers that advertise themselves as AppSocket/JetDirect (the common type for network receipt printers); an IPP-only printer, or one connected by USB, won't show up and still needs its address or device path entered manually.
3. Use the test print button to check the connection.
4. A kitchen printer can be set separately so food orders print where they are prepared — the same **Find printers on this network** button offers a candidate for either field.
5. Need more than one kitchen printer — a grill printer and a bar printer, say? See **Kitchen stations** to route categories or individual items to their own station.
6. Kitchen tickets print the order type and station header in the till's configured language, rather than always in English. There's no separate kitchen-only language setting — it follows the till's one configured locale.
7. Cash drawer not opening after a cash sale? Most drawers are wired to pin 2 (the default) — if yours needs pin 5, set it under **Cash drawer pin** in the printer settings.
8. Currency symbols printing garbled — `€` or `£` coming out as strange characters? Many thermal printers don't understand UTF-8. Since v0.12.12 the till picks this for you: a shop configured in euros or pounds, with a Western European language, sends **Western Europe (CP858 — €/£)** automatically, and a till that was already set up is switched over once, the first time it starts on this version. A shop in euros or pounds with a Croatian, Slovenian or Slovak language sends **Central Europe (Windows-1250)** the same way (pounds only switches when the currency symbol is actually covered — Windows-1250 has no `£`, so a Croatian/Slovenian/Slovak shop in pounds stays on UTF-8); Estonian, Latvian or Lithuanian sends **Baltic (Windows-1257)**; Greek sends **Greek (Windows-1253)**; and Turkish sends **Turkish (Windows-1254)** — all four cover both `€` and `£`. You only need **Characters** in Settings → Printer if you want to override that — for example a printer that genuinely does handle UTF-8. This only changes what is sent to the receipt printer — the till's language support on screen is unaffected. A shop billing in Turkish lira (`₺`) stays on **UTF-8**: none of the code pages above can print the lira sign, so switching wouldn't fix the garbling. Arabic and Farsi tills likewise stay on **UTF-8** and are never switched automatically — no single-byte code page here covers those alphabets.
9. **Receipt after each sale** (Settings → Printer) decides what happens to the paper receipt when a sale completes: **Always print** (the default — every sale prints), **Ask the customer** (the receipt screen shows *Would you like a receipt?* with **Print receipt** / **No receipt** — the sale is already complete either way, and the usual Print button stays available below), or **Never print automatically** (the cashier prints from the receipt screen when needed). Shops in Germany are fixed to Always print for now, while the receipt rules for German shops are being confirmed — an installed country plugin can likewise limit the choices to what its market permits.
