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

## Gift vouchers on the day-end (Z) report

When your shop sells or accepts multi-purpose gift vouchers, the printed
day-end report shows a separate **GUTSCHEINE** section: how many vouchers
were issued and redeemed that day, and for how much. Selling a voucher is
recorded as money owed to the voucher's future bearer, not as product
revenue — so the amount appears in the day's overall takings but never in
the per-department or per-tax-rate product figures. Tax on the goods is
recorded when the voucher is later spent, at those goods' own rates, the
same as if the customer had paid cash. The section only prints on days
with voucher activity.

Voiding the sale that sold a voucher cancels the voucher with it, as long
as the voucher is still unused — it disappears from the report and can no
longer be spent. If any part of the voucher has already been spent, the
till refuses to void that sale: sort out the outstanding voucher with the
customer first.

## Cancellations, and who ran the close

The printed day-end (Z) report shows a **STORNOS** section, separate from
Refunds, on any day at least one sale was voided: a cancellation here
means a completed sale that was voided/reversed afterwards (e.g. a
same-day correction), while a refund is a formal return processed
afterward. The two mean different things to an auditor, so they're never
mixed into one figure — voiding a sale already carries no revenue and
never changes the day's Net. The section is absent entirely on a day with
none.

The report also always prints **Erstellt von** (who ran the close) — the
person's display name, or "System" for the automatic scheduled close — so
there's a record of who closed the day even without checking the Audit
page. An optional note can currently be attached to a close by whatever
sends the request an `annotation` value; there's no on-screen field for it
yet, so this is mainly useful to an integration or a future till update —
it prints as **Anmerkung** immediately under Erstellt von when present.

## Payment method and VAT rate together on the day-end (Z) report

The printed day-end report includes a **BY METHOD & VAT RATE** table: the
day's takings broken down by payment method and VAT rate at once — one row
per combination (e.g. cash at 7%, card at 19%), each with its net, tax and
gross amount. This is the grid an accountant posts into bookkeeping
software: which payment method the money arrived through, against which
VAT rate. The rows always add up exactly to the day's per-VAT-rate totals.
When a sale was paid with more than one method, its amounts are split
across those methods in proportion to what each method paid. Tips are not
included here — a tip carries no VAT — so a card row group can total less
than that card's takings line by exactly the day's card tips, and less
still on a day with a card refund (a refund reduces the card takings line
too, so the two stay in step).

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
Enter the amount removed as a negative number (e.g. "-50" for a 50-unit
payout) — on a touch till with no physical keyboard, tap the on-screen
keyboard's "-" key first.

On a German shop in system-of-record mode, an adjustment that removes cash
— and a Pfandrückgabe payout — also goes through the same TSE check a sale
or a refund does, so it is refused while no TSE is set up or the TSE is
failing. See **Selling** → "German shops: TSE and real sales" for what that
message means and how an owner can grant a temporary override. Adding cash
is never affected.

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

## Counting the drawer at close: skim & new float

The opening cash for a new shift is **carried over automatically** from
the register's last close — whatever the previous close left in the
drawer is pre-filled, so you confirm it rather than re-type it. You can
still edit the figure if the drawer was corrected in between; whatever
you submit is what's recorded.

When you close a shift, count the drawer and enter the counted cash as
before. Two optional extras join it:

- **Skim to safe** — the amount you move from the drawer to the safe as
  part of the close. The counted cash minus the skim becomes the drawer's
  **new float**, which is what the next shift on that register opens with.
  A skim can't exceed the counted cash, and it never changes the expected
  figure — the variance always compares your count against takings
  *before* the skim, so moving money to the safe can't hide a shortage.
  An optional reason can be recorded with it.
- **Denomination count** — an optional per-denomination count (how many
  of each coin and note) stored with the close as a count protocol, for
  shops that want the till count documented piece by piece. Leave it
  empty to skip it entirely.

## Cash reconciliation on the day-end report

The printed end-of-day (Z) report gains a **CASH RECONCILIATION**
section on any day at least one shift was closed: opening float, cash
sales, tips held out (only printed on a day that actually has a cash
tip), pay-ins, pay-outs, calculated (what should be in the drawers),
counted (what was in them), variance, skim to safe, and the new float
carried to the next day. Cash sales excludes any cash tip the same way
tips are already held out of revenue elsewhere on the report — that is
why the "Tips held out" line sits between cash sales and pay-ins:
opening float + cash sales + tips held out + pay-ins + pay-outs together
equal calculated, so the section's own figures still add up once cash
tipping is in use, not just on an ordinary no-tip day. Skim to safe is
entered as part of closing the shift, after calculated is already fixed
for the day, which is why it is listed below variance rather than folded
into that sum. A non-zero variance is flagged with
`!!` on the printout, and the Day-end tab marks that day's row with a
warning tag so a discrepancy is visible on screen without reprinting
each period. A day with no closed shift still produces a complete
report — the section is simply absent, and running End of day is never
blocked on closing a shift.

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
