---
id: reports
title: Reports & end of day
section: Running the business
order: 210
summary: Sales totals by day, department and payment type; best and slow sellers; dead stock; busiest days and hours; margins; tax summary; year-over-year — plus the end-of-day (Z) report for cashing up.
routes: [/reports, /journal, /journal/{receipt}, /shifts, /audit]
keywords: [z report, end of day, takings, journal, shift, audit]
---

# Reports & end of day

Sales totals by day, department and payment type; best and slow sellers; dead stock; busiest days and hours; margins; tax summary; year-over-year — plus the end-of-day (Z) report for cashing up.

## How to use it

1. Open Reports: the row at the top always shows your key numbers for the chosen period (revenue, sales, tax, refunds, net, last year) and a low-stock warning.
2. Pick a tab below it — Sales trend, Items, Tax, Forecast, Payments & channels, or Day-end (EOD) — and that report loads when you open it.
3. Run End of day (in the Day-end tab) when you close: it totals the day and can print for your records.

## Report periods

Next to the top row, pick how you want the period worked out:

- **Custom** — the original rolling window (today, 7/14/30/90 days back from now).
- **Day / Week / Month / Year** — a real calendar period instead of a rolling
  count: Day is one trading day, Week is Monday–Sunday, Month is a calendar
  month, Year is a calendar year. A date picker appears next to the choice so
  you can look at a past period — e.g. pick Month and a date in July to see
  July's numbers, even if today is in August.
- Sales trend, Items, Tax and Payments & channels use whichever period is
  selected, so they always agree with the numbers at the top. Forecast and
  Day-end's archive list don't — they show their own fixed windows regardless
  of the period picker.

## Business day start

By default a report "day" runs midnight to midnight. If you trade past
midnight — a bar, a late kitchen — that splits one night's takings across
two report days. Set **Business day starts at** (in the Day-end tab,
alongside the automatic end-of-day time) to when your trading day actually
begins, e.g. 06:00, and Day/Week/Month/Year periods line up with your real
trading day instead of the clock.

## Report retention

Every archived end-of-day report is kept for **10 years** — this is a legal
record, not something the "Clear transaction history" reset button in Data
management can remove. Once a report passes its 10-year anniversary, it is
automatically and permanently deleted in the background — there is no
manual step and no confirmation prompt, so export anything you need to keep
longer before then.

In Settings → Report Retention, choose where reports are kept:

- **This till only** — works today, no extra setup. Report archives are
  small (a few KB per closed day), so keeping 10 years of them won't fill a
  modern till's disk.
- **Cloud only** / **Till + cloud** — shown for a future release once cloud
  storage and a shop subscription are available; not selectable yet.

The same page shows **how far back your records go** (earliest to latest
archived report, and how many) and an **export** button — pick a date
range and download the matching reports as CSV or JSON, e.g. to hand to an
auditor.

## Cash adjustments & payouts (Shifts)

The Shifts page's "Cash adjustment / payout" form records anything that
changes the till's expected cash outside of a sale — a float top-up, a
till-count correction, or cash paid out of the drawer. Any adjustment
that **removes** cash needs a manager PIN, whichever type is selected —
the same approval a refund or a bottle-deposit (Pfandrückgabe) payout
needs, since it's the same risk (cash leaving the drawer unapproved).
Adding cash (a positive amount, e.g. a float top-up) doesn't need one.
