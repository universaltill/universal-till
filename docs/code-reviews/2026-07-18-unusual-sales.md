# Code review — unusual-sales alert (till detection) (2026-07-18)

Branch `feat/unusual-sales-alert`. Second predictions alert type (mp side —
allowlist, inbox row, localized mail — merged separately).

- `POSRepo.DayTotal(daysAgo)` (local-time calendar day, completed sales).
- `alerts.unusualSales`: yesterday vs the SAME WEEKDAY's average over the
  previous 4 weeks; requires ≥3 baseline weeks with sales (no noise for new
  shops); flags >1.8× (blowout) or <0.4× (collapse — including a zero day
  on a normally-selling weekday).
- Daily alerts tick pushes `unusual_sales` {ratio_pct, total}; `pushNotify`
  extracted as the shared store-token push (digest refactored onto it).

Tests: baseline/normal/zero-day/blowout matrix; existing digest test
unchanged. Suites + both guards green.
