# Code review — year-over-year KPI (2026-07-17)

Branch `feat/yoy-report`. Owner-reports increment: the reports page KPI row
gains "vs last year" — the selected period compared with the SAME window one
year earlier (`PeriodComparison`; completed sales only, returns excluded).
Percentage delta (negative flagged red) + then→now totals. The card renders
ONLY when year-ago data exists (`YoYHas`) — new shops see nothing rather
than a meaningless +0%. i18n ×4. Suites + guards green.

Note: becomes meaningful for Farshid's shop once it has a year of history;
correct from day one for any shop restored/migrated with history.
