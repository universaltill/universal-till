---
id: voucher-import
title: Import voucher balances
section: Setting up your shop
order: 148
summary: Migrating from another till? Bring in the outstanding balances on physical voucher cards your customers already hold, as an opening balance — not a new sale.
keywords: [voucher, gift voucher, gift card, gutschein, import, migration, balance, opening balance, csv]
routes: [/settings/vouchers/import]
---

# Import voucher balances

If you're moving to this till from another system, some of your customers may already be holding physical voucher (gift card) cards with real money left on them. This page brings those balances in as an **opening balance** — money this till now owes those customers — without recording a new sale. See [Gift vouchers](/help/vouchers) for how a voucher works day to day once it's in.

## How to use it

1. Open **Settings** and, under **Data management**, choose **Import voucher balances** (manager only).
2. Prepare a CSV file with a **code** column (the code printed on each physical card), a **balance** column (what's still left on it), and an optional **label** column naming who holds it. Column names are flexible — "amount" works as well as "balance", "holder" as well as "label" — as long as one column clearly names each.
3. Choose the file and press **Preview**. Nothing is saved yet: you see how many vouchers will be created, their total value, and a list of any rows that couldn't be read, each with the reason.
4. Check the preview, then press **Confirm import**. Every valid row becomes an active voucher on this till, ready to be spent like any other.

## Good to know

- A code that already exists on this till (from an earlier import, or one this till issued itself) is never overwritten — that row is skipped and reported, so re-uploading the same file by mistake can't double the balance.
- A row with no code, an unreadable balance, or a balance of zero or less is skipped and reported; every other row in the file still imports.
- A code repeated more than once in the same file is skipped entirely, both times — fix the file and re-upload rather than guessing which one is right.
- Imported vouchers work exactly like ones sold at this till — see [Gift vouchers](/help/vouchers) for redeeming them and taking them as payment.
- Importing does not count as a voucher "sold" on the day-end report, since no sale actually happened here — see [Reports & end of day](/help/reports).
