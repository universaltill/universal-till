# Review — flaky counter-orders Age test (ut-docs#2749)

- **Change:** `internal/pages/kiosk_counter_orders_page_test.go` — the "no absolute date" check strips the order's random UUID before looking for the current year, and adds a date-shape regex (`dd/mm/yyyy`, `dd.mm.yyyy`, `yyyy-mm-dd`).
- **Why:** release v0.23.0 (run 36135531534) failed because the UUID `…c01e2026f39a` contained "2026". The rendered Age column was correct ("42 min").
- **Reviewer:** Claude Sonnet 5 (different model from the author, Opus 5.5).
- **Findings:** none. The reviewer confirmed a regression to `09/11/2026 03:05` or `2026-09-11` still fails the test, and that the template's rendered fields (`DisplayNo`, order type, items summary, `AgeMinutes`) cannot produce a false date-shape match.
- **Verification:** `go test -count=20 -run TestKioskCounterOrdersPage ./internal/pages/` passes 20/20.
