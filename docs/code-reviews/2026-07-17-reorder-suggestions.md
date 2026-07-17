# Code review — reorder suggestions (2026-07-17)

Branch `feat/reorder-suggestions`. Predictions increment: rows already
flagged "runs out ≤7 days" now also show **"order ~N"** — the quantity that
refills to a 14-day cover at the current 28-day selling rate
(ceil(rate × 14 − on-hand)). Constant cover target documented as the
default until per-item lead times exist (queued with the seasonal
forecasting arc). Suggestion renders only on running-out rows (no noise on
healthy stock). i18n ×4 (with explanatory tooltip), logical CSS pill.

Test extends the prediction test: 2/day × 14 − 6 = 22 asserted in the
rendered page. Suites + guards green.
