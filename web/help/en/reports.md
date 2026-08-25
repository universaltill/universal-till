---
id: reports
title: Reports & end of day
section: Running the business
order: 210
summary: Sales totals by day, department and payment type; best and slow sellers; dead stock; busiest days and hours; margins; tax summary; year-over-year — plus the end-of-day (Z) report for cashing up.
routes: [/reports, /journal, /journal/{receipt}, /shifts, /audit]
keywords: [z report, end of day, takings, journal, shift, audit, tips, tronc, service charge, worker allocation]
---

# Reports & end of day

Sales totals by day, department and payment type; best and slow sellers; dead stock; busiest days and hours; margins; tax summary; year-over-year — plus the end-of-day (Z) report for cashing up.

## How to use it

1. Open Reports: the row at the top always shows your key numbers for the chosen period (revenue, sales, tax, refunds, net, last year) and a low-stock warning.
2. Pick a tab below it — Sales trend, Items, Tax, Forecast, Payments & channels, Tips, or Day-end (EOD) — and that report loads when you open it.
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

This shift also applies to Sales trend's busiest-hour chart — a sale made
just after midnight can show under a pre-midnight hour label (e.g. 22:00)
rather than its real clock time, consistent with Day/Week/Month/Year
already counting that sale as part of the previous business day.

## Report retention

Every archived end-of-day report is kept for **10 years** — this is a legal
record, and the "Clear transaction history" reset button in Data management
never touches it. (That button no longer destroys anything at all: it moves
your transaction history into a reset archive, and an archived batch can be
restored from Settings → Data as long as the till has not traded since the
reset.) Once a report passes its 10-year anniversary, it is
automatically and permanently deleted in the background — there is no
manual step and no confirmation prompt, so export anything you need to keep
longer before then.

A reset archive batch can also be **permanently deleted** from the same
Settings → Data list, but only once it is old enough: a batch holding real
sales is protected until your shop's country's retention window (set on
the Country settings page) has passed since it was archived — deleting it
earlier is refused, and the message tells you the date it becomes
deletable. A batch with no sales in it at all (nothing was sold yet when
it was reset) deletes right away even if it still holds other test data,
such as till shifts — the protection is specifically about sales records.
This is separate from, and does not change, the 10-year report retention
above.

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

A bottle-deposit (Pfandrückgabe) payout is recorded against **this till's
own register's** open shift — even when another register's shift is open
at the same time, it never lands on the other drawer. On a shop with more
than one register, the till has to know which register it is first: set
"This till's register" in Settings → Tills, or the payout is refused with
a message pointing you there.

Opening a new shift shows a register picker too. On a shop with more than
one register it now defaults to this till's own register (set under
Settings → Tills), so opening a shift on the till you're standing at
normally needs no picking — you can still choose a different register
from the list for the rare case a shift needs opening on one.

The Payments & channels tab on Reports shows a **Cash adjustments by
reason** breakdown for the selected period — e.g. a total for
"Pfandrückgabe" across every payout in that window — so you can see a
figure like "total bottle deposits paid out this week" without opening
the Audit page. It only appears once there's at least one adjustment in
the period.

## Seeing every till's sales (Journal)

The Journal page (the receipts/sync list, off the sale screen) shows every
till's sales by default, newest first, with the till that took each order
shown in its own column — so one machine can review the whole shop's
takings without walking to each register. Switch it to "This till" to see
only this machine's own sales instead.

Use the filter row above the list:

- **Till** — "All tills" (the default) shows every till's sales, newest
  first; "This till" narrows the list to only this machine's own local
  sales; or pick one specific till by name to see just its sales.
- **Day** — pick a date to narrow the list to that calendar day; leave it
  blank to see the most recent sales regardless of day.

When any till other than "This till" is enrolled, a line under the filters
shows each enrolled till's name and when it was last in contact with this
machine ("last contact from *till*: *time*") — a till that's pinging fine
can still have a failed sales sync underneath, so this is a network-contact
signal, not proof its sales have actually arrived; useful for spotting a
till that's fallen behind, not a substitute for reconciling totals. A till
shows "—" here if it's never been in contact, or (on a replica) because
its contact time isn't shared down from the primary.

Only a shop's primary till accumulates other tills' sales — a replica till
only ever has its own local sales to journal, whatever till filter is
picked, because a replica only ever pushes its own sales one-way up to the
primary and never receives siblings' sales back down. Picking "All tills"
or a specific other till on a replica shows a message explaining that
cross-till sales are only available on the shop's primary till, instead of
a table that's quietly empty with nothing to explain why.

## Worker tip and service-charge payouts (Tips tab)

The **Tips** tab records how tips and service charges are paid out to
workers, and reports on it — one part of the record-keeping the UK
Employment (Allocation of Tips) Act 2023 asks employers to keep. It records
what a manager tells it: the software does not detect or move any money on
its own.

- **Received vs allocated** — two totals for the selected period: what came
  in (tips on completed sales, or service charge once that's collected) and
  what's been recorded as paid out to a worker. They run on different
  clocks — money received today might not be paid out until a later shift —
  so the two figures not matching on a short window is normal, not a
  problem by itself; check over a window wide enough to cover both.
- **Recording a payout** — a manager (Worker payouts permission) picks the
  worker, the date the money was actually paid, tip or service charge, the
  amount, and an optional note, then submits. The date can't be in the
  future — this records a payment that already happened.
- **A worker's own records** — use the Worker filter to narrow both the
  totals and the payout list to one person, e.g. to show a worker (or show
  them, on request) what's been recorded as paid to them.
- **Export** — download the payout records for a date range (optionally one
  worker) as a CSV file, for handing to a worker, an accountant, or anyone
  else who needs the underlying records rather than just the totals.
- Payout records are kept alongside the shop's other financial records and
  are not deleted early — the same retention as everything else on this
  page (see Report retention above).
- **On the printed Day-end (Z) report** — the report also prints a short
  tips-by-payment-method line (e.g. "4x Card £3.20") for any day with at
  least one payment that recorded a tip — most often a card payment
  where the terminal's own tip prompt was used. This is held separately
  from the day's sales totals, not counted as revenue. It can read
  differently from "Received" above: the Z-report line counts every
  tipped payment regardless of who the tip belongs to, while "Received"
  only counts tips recorded for the employee (the default) — the two are
  expected to differ once a tip is recorded for the business instead.

## Reconciling a card payment (receipt detail)

Opening a receipt from the Journal shows its full payment detail — this is
where a card payment gets reconciled after the fact, days after the sale.
When a payment was taken on a card-present terminal, its payment row shows
the masked card number and approval code (the same reconciliation line the
printed receipt showed at the moment of tender), plus the terminal and
trace ID for matching it against the terminal's own settlement report.
These fields only appear once a payment method actually records them —
today's built-in payment methods (cash, Stripe, SumUp, QR pay) don't, so
existing receipts are unaffected.
