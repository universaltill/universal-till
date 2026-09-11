---
id: invoices
title: Invoices & tax invoices
section: Running the business
order: 220
summary: Issue a proper (tax) invoice for a sale — for business customers or wherever a titled invoice is required.
routes: [/invoices, /invoice/{display_no}]
keywords: [invoice, vat, customer, business, tax invoice, credit note, seller details]
---

# Invoices & tax invoices

Issue a proper (tax) invoice for a sale — for business customers or wherever a titled invoice is required.

## Turning it on

Invoicing is off until a manager fills in the business details it prints
on every invoice:

1. Go to Settings → Invoices.
2. Fill in **Business name** (required — this is what turns the feature
   on), **VAT number**, and **Business address**.
3. Save.

A registered VAT number changes the document's own title to a proper
"Tax Invoice"; leave it blank and a plain invoice is issued instead.
Clearing the business name later turns the feature back off.

## Issuing an invoice

1. Find the sale in the Journal and open it.
2. Under **Issue a VAT invoice**, fill in **Customer / company** (required),
   and their address and VAT number if they need to appear on the
   document (both optional).
3. Press **Issue invoice**. It's created, numbered, and (if a receipt
   printer is configured) printed automatically, with a link to view it.

Issuing again for the same sale never creates a duplicate — it just shows
you the existing invoice number and a link to it. Only a **completed**
sale can be invoiced; refunds and voided/incomplete sales don't offer the
form.

## Credit notes

Refunding a sale that already has an invoice automatically issues a
credit note against it, in the same numbering series — nothing to do by
hand. It's listed in the invoice register alongside ordinary invoices,
and its own page links back to the original invoice it credits (the
original invoice's own page has no matching link forward — find the
credit note from the register, or from the original's linked sale in the
Journal, if a refund was issued there).

## Viewing, reprinting and exporting

1. Press **🧾 Invoices** on the Journal page (only shown once invoicing is
   turned on) to open the register: every invoice and credit note issued,
   newest first, with a **Range total (credit notes subtracted)** for the
   list currently shown.
2. Pick a date range (**From**/**To**) and press **Show** to widen or
   narrow it — it opens showing the current calendar month; widen
   **From** to reach older invoices.
3. Open any row to view or reprint that invoice/credit note.
4. **Export CSV** downloads the current date range as a CSV for handing
   to an accountant. On screen a credit note's amounts show as ordinary
   positive numbers (only the range total subtracts them); in the CSV
   they're written as negative amounts instead, so the file's own column
   totals already net out correctly without further arithmetic.

## What can go wrong

- **No "Issue a VAT invoice" form on a receipt** — either invoicing isn't
  turned on yet (see Turning it on above), or the sale isn't a completed
  sale (a refund or a voided/incomplete sale can't be invoiced directly —
  refund an invoiced sale instead and its credit note is automatic).
- **Opening Invoices sends you to Settings instead** — the business
  details haven't been filled in yet; fill them in and the register opens
  normally from then on. (Opening it as a till operator, rather than a
  manager, instead sends you to the Journal — invoicing is a manager
  feature.)
- **Customer / company left blank** — the form refuses to submit; it's
  the one required field.
