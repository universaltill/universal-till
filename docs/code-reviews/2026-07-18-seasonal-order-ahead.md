# Code review — seasonal order-ahead report (2026-07-18)

Branch `feat/seasonal-order-ahead`. The headline Phase-1 forecasting ask
(Farshid: "look at previous years' sales and understand what the shop needs
to order in advance") — first working increment, honest by construction.

- `POSRepo.SeasonalUpcoming(days=28)`: items sold in the SAME upcoming
  window ONE YEAR AGO (variant lines folded into parents), with current
  total on-hand and a suggested top-up (ceil(lastYear − onHand), ✓ when
  covered). Pure statistics per the self-hosted-AI rule; retention already
  verified (sales never pruned).
- Reports card "Coming up (based on last year)" — HIDDEN entirely for shops
  without year-old history (no fake numbers for new shops; Farshid's shop
  will light up as its data ages). i18n ×4.

Test: fresh shop empty; a 358-day-old sale of 20 with 5 on hand → order 15.
Suites + guards green.

Follow-ups queued with the arc: multi-year averaging when 2+ years exist,
holiday-shift awareness (lunar calendar events like Ramadan move ~11 days/yr
— relevant to Farshid's markets), category rollups.
