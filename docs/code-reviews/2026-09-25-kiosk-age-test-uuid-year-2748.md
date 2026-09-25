# 2026-09-25 — Kiosk counter-orders age test: no false fail on a UUID holding the year (ut-docs#2748)

**Shipped:** `TestKioskCounterOrdersPage_ColumnShowsElapsedAgeNotAbsoluteTimestamp`
removes the seeded order's id from the body before its "no current year"
check. The body carries the order's random UUID (row id, collect URL), which
contains the year's digits roughly once in a thousand runs. It failed the
`main` release run 36135531534 that way (`c544d9cc-ffb9-4732-a120-c01e2026f39a`).
Test-only change.

**Review:** Sonnet (author: Opus 5.5). It found no issues:
- `created.ID` is byte-for-byte what the partial renders (`kiosk_counter_orders_list.html:26,41`).
- No other generated value (token, nonce, hash) is in this fragment.
- Regression proof: adding `(2026-09-25)` to the Age cell makes the test fail. Reverted afterwards.

**Verdict:** safe to merge.
