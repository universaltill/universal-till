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
8. Currency symbols printing garbled — `€` or `£` coming out as strange characters? Many thermal printers don't understand UTF-8. Under **Characters** in Settings → Printer, choose **Western Europe (CP858 — €/£)** and those symbols print correctly. This only changes what is sent to the receipt printer — the till's language support on screen is unaffected. CP858 covers Western European letters only: Arabic, Farsi and Turkish characters cannot be printed on it, so leave **UTF-8** selected if your receipts carry them.
