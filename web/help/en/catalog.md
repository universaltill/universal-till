---
id: catalog
title: Catalog, variants & barcodes
section: Setting up your shop
order: 110
summary: "Your products: names, prices, departments, item variants (size, flavour…) and any number of barcodes per item or per variant."
routes: [/catalog, /import, /designer]
keywords: [items, prices, barcode, variants, modifiers, import, export]
---

# Catalog, variants & barcodes

Your products: names, prices, departments, item variants (size, flavour…) and any number of barcodes per item or per variant.

## How to use it

1. Open Catalog and click an item row — the editor panel below shows all its variants and barcodes. Items with a SAMPLE badge came from the optional starter catalogue (removable from Settings → Data).
2. Edit variant names, SKUs, prices, cost prices and photos right in the grid; add or remove barcodes as chips; print labels per variant.
   - **Plain code (ignore weight/price):** tick this when adding a barcode only if your shop uses weight- or price-embedded scale labels *and* you are entering an ordinary product barcode that happens to start with the same digits. It tells the till to store the code exactly as typed instead of reading part of it as a weight or price. Leave it unticked for everything else — it has no effect on a normal barcode.
3. Use Import to load items from a CSV file, and Export to save your catalog — a speedy kasse / pepperm cashbox `.bkp` till backup is also accepted directly, detected automatically with no conversion needed. Either file can also carry a `Tax rate` column (and a `Takeaway tax` column where it differs) — Universal Till creates the matching tax codes for you automatically.
4. A takeaway tax rate needs the German tax plugin enabled to save its override — if it's installed but switched off, the import still creates correct tax codes but the summary warns that the takeaway override wasn't applied; enable the plugin and re-run the import to fix it.
