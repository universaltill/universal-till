# Code review — busiest days & hours report (2026-07-17)

Branch `feat/busy-times-report`. Next owner-reports increment (staffing:
"when is the shop busy").

- `POSRepo.SalesByWeekday` / `SalesByHour` — completed sales bucketed by
  LOCAL weekday/hour (strftime 'localtime'; returns excluded) over the
  selected period; shared `busyBuckets` helper.
- Reports page: two cards with pure-CSS horizontal bars (busiest bucket =
  100%, normalized in the handler so the template stays dumb), count per
  bucket. Weekday names via locale keys ×4; logical CSS (RTL bars grow the
  right way).
- Test extends TestSlowItemsAndDeadStock: one seeded sale lands in exactly
  one weekday + one hour bucket, slots range-checked.

Suites + guards green.
