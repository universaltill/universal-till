# Code review — daily low-stock digest push (till side) (2026-07-18)

Branch `feat/low-stock-digest-push`. Notifications increment 1, slice 3
(the marketplace inbox + endpoint shipped in the mp repo).

- `internal/alerts`: `runningOutCount` (same 28-day/≤7-day model as the
  inventory page), `pushDigest` (registered tills only; nothing running out
  = no push; store token auth), `Start` (2-min post-boot delay for
  enrolment, then every 24h; ctx-cancelled; failures logged and retried
  next day — never touches the sale path, per the offline-first rule).
- Wired in main after updates.Start.

The owner sees "⚠ Low stock — N item(s) running out soon" in My shop.
Test: real migrations + fake marketplace asserting payload/token, plus the
unregistered no-push path. Suites + both guards green.
