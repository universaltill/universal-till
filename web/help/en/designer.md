---
id: designer
title: Receipt designer
section: Running the business
order: 240
summary: Customise what your receipt shows — logo, header/footer text, and which sections print — with a live preview and a test print before you commit.
routes: [/receipt-designer]
keywords: [receipt, logo, layout, footer, header, preview, print test]
---

# Receipt designer

Customise what your receipt shows — logo, header/footer text, and which sections print — with a live preview and a test print before you commit. Managers and admins only: Settings → Receipt printer shows a **Receipt designer** button, but only to a signed-in manager or admin; a cashier viewing that same settings page doesn't see the button at all, and opening the page's address directly just sends a cashier back to Settings.

## How to use it

1. Open **Receipt designer** from Settings → Receipt printer.
2. Fill in up to three **Header lines** (address, phone number, VAT number — whatever you want printed above every sale) and a **Footer message** (e.g. "Thank you!") — all four fields are capped at 42 characters so they fit a narrow receipt roll.
3. Tick which sections print: **Show item codes (SKU)** next to each line, **Show subtotal and tax lines**, **Show the receipt barcode** (needed for scan-to-refund — turning this off means a receipt can't be scanned to start a refund), and **Print the logo at the top**.
4. The **Preview** on the right updates automatically as you type or tick a box (about a third of a second after you stop) — it's a sample sale, not a real one, so nothing about it touches your actual data.
5. **Print test receipt** sends a real receipt — including any change you haven't saved yet — to your configured printer right now, so you can hold the paper copy before committing to it. It is not always exactly what the preview shows: the preview is plain text and can never render a logo, but a test print includes one if **Print the logo at the top** is ticked (see Logo, below). It uses the same printer set up in Settings → Printer, so it fails for the same underlying reasons as a test print from there — but this button's own failure message is just "Print failed," with no further detail. If it fails here, run **Settings → Printer**'s own test print instead and check "Nothing is printing" in [Receipts & printing](/help/printing) for what the detailed message means.
6. **Save design** is what actually changes real customer receipts from then on — Print test receipt never does this by itself. Until you press Save, real sales keep printing the previous design even while the preview and test prints show your new one.

## Logo

- Upload a logo below the main form: PNG works best (simple black-on-white art prints sharpest); JPEG and GIF are also accepted. It's validated by actually reading it as an image, so a renamed non-image file is rejected with **"that file could not be read as an image"**, and anything over about 4 MB is rejected too.
- Uploading (or removing) a logo takes effect immediately — it doesn't wait for Save design. Only whether it **prints** is controlled by the saved **Print the logo at the top** setting: ticking that box makes a test print include the logo (a real receipt only gains it once you've saved with the box ticked) — but the on-screen **Preview never shows the logo either way**, since it's rendered as plain text and has no way to draw an image. Don't judge whether the logo will print by looking at the preview; run a test print instead.
- Ticking **Print the logo at the top** before you've uploaded anything doesn't error — it simply prints nothing extra until a logo exists to print.

## What can go wrong

- **The page redirects you back to Settings with no error message.** This means you're signed in as a cashier, not a manager or admin — there's no PIN prompt to unlock it here, unlike most other manager-only actions in the till.
- **Print test receipt fails.** It shares the exact printer setup with Settings → Printer, so every cause in [Receipts & printing](/help/printing)'s "Nothing is printing" section can be the reason — but this button only ever shows a bare "Print failed," not the specific reason. To see which of those causes it actually is, run the test print from **Settings → Printer** instead.
- **Arranging sale-screen buttons isn't done here.** This page only designs what prints on receipts. To arrange the quick-sale buttons and product grid shown on the sale screen, see [Quick Buttons](/help/till-designer) instead — a separate page, despite the similar name.
