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
   - **Fixing problem rows before import:** the Preview lists every row that would be skipped and why. A row skipped only because its name is missing or its price could not be read can be fixed right there: tick *Import anyway* on that row and type the corrected name or price, then press Import — the corrections are applied and the row imports with the rest. All other skipped rows (for example a barcode or SKU that already exists in your catalog) always stay skipped — that is what keeps importing twice safe.
   - **A `.bkp` till backup that reuses one item number across several different products** (common on older tills) no longer loses the extra items: the first one keeps the original number and every later one imports under a new, generated number instead of being dropped — the import summary tells you which rows this happened to, so you can give them a tidier number later if you want.
   - On a long file the Preview list can run to hundreds of rows — an Import button is repeated at the bottom of the list so you don't have to scroll back to the top to continue. Once you press it, a green "Imported ✓" summary with a View catalog button confirms the import actually happened and takes you straight to your items.
4. A takeaway tax rate needs the German tax plugin enabled to save its override — if it's installed but switched off, the import still creates correct tax codes but the summary warns that the takeaway override wasn't applied; enable the plugin and re-run the import to fix it.
5. The first time you import priced items on a till whose currency you've never explicitly set, Import stops before writing anything and asks you to confirm which currency the file's prices are in — a fresh till defaults to GBP, so importing a foreign catalogue without checking this could otherwise price everything under the wrong currency. Confirm the currency shown if it's right, or pick the correct one; either way Import proceeds automatically and won't ask again once your till's currency has been set.
6. Which barcode types a scan or manual entry accepts is controlled per shop from Settings → Barcode types. Every common retail type (EAN-13, EAN-8, UPC-A, UPC-E, GTIN-14, Code 128, Code 39, internal/PLU codes) is on by default, so existing barcodes keep working exactly as before. The two scale-label types (weight-embedded and price-embedded EAN-13) are off by default — turn one on only once you're ready to use scale-printed labels, since it changes how a matching code is read.
7. In the screen designer, search for an item and tap it to add a quick-sale button; tap the ▲/▼ arrows on a button to move it earlier or later in the sale screen's grid — this works the same by touch, mouse or keyboard, so it's usable on the till itself, not only from a desktop.
