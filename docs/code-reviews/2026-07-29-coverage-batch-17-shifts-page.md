# Test coverage batch 17: shifts page render

2026-07-29

`internal/pages/shifts_page.go` — the `GET /shifts` read-only page:
current open shift, shift history, and the register list for opening a
new shift. Previously zero coverage. (`shifts_api.go` — the
open/close/adjustment handlers — is a separate file with its own
pre-existing tests, not part of this batch.)

## What's covered

- No open shift: "No open shift" banner, no Current Shift card, and the
  register list (for the open-new-shift form) renders correctly.
- An open shift + one closed historical shift: open-since banner,
  Current Shift card (register, opening cash), and the closed shift's
  counted cash, expected cash, and variance in the history table.

The test harness (`newShiftsPageTestDeps`) explicitly calls
`httpx.InitI18n` before rendering, following the pattern established in
batch 16 (`hold_api_test.go`) — every text assertion in this file is
translated copy (`shifts.none_open`, `shifts.open_since`,
`shifts.current`), so without the explicit init these would only pass by
accident, riding on some other test file in the package having already
initialised i18n first in the same test binary run.

## Independent review (opus) — one real gap closed, two cheap additions

1. **The `£12.00` assertion didn't isolate what it claimed to test.**
   The closed shift was originally seeded with `closing_cash=1200` AND
   `expected_cash=1200` (equal), so `£12.00` appeared in both the
   Counted and Expected columns and the test would have passed even if
   the template swapped them. Fixed: seeded distinct values
   (`closing_cash=1200`, `expected_cash=1500`) so `£12.00` uniquely pins
   Counted, `£15.00` pins Expected, and a genuine non-zero variance
   (`£-3.00`) is now observable and asserted — confirmed against
   `FormatMoney`'s actual negative-number rendering (GBP hugs the symbol
   to the number: `£-3.00`, not `-£3.00`).
2. **`Registers` was never asserted** despite being real handler output
   (the no-open-shift form's register `<select>`) — cheap to add given
   the existing scaffolding, folded into the no-open test.
3. Nitpick: `context.Background()` → `t.Context()` for consistency with
   the rest of the harness.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
