# Test coverage batch 4: shift open/close cash reconciliation

2026-07-29

Fourth batch of the pos_repo.go backfill: the shift open/close chain and
end-of-shift/end-of-day cash-reconciliation math — `FindOpenShiftForRegister`,
`InsertShift`, `LoadShiftForClose`, `UpdateShiftClose`, `ShiftOpenExists`,
`SumCashPaymentsForShift`, `SumShiftAdjustments`, `ComputeExpectedCash`. High
business risk: wrong window/summation logic means a cashier's till either
"doesn't balance" for no real reason, or a genuine shortage goes unflagged.

## What changed

`internal/data/pos_repo_shift_close_test.go` (new, package `data`, reuses
the `newPOSLifecycleTestDB` helper from batch 2). Covers:

- Opening a shift and finding it as "the" open shift for its register (and
  correctly finding none for a different register).
- Loading a shift for close, and refusing to load/close an already-closed
  shift (can't close twice).
- `SumCashPaymentsForShift`'s time window: payments before the shift opened
  or after it closed are excluded; a card payment inside the window is
  excluded (only `payment_methods.type='cash'` counts); a still-open shift
  (no `closedAt`) has no upper bound.
- `SumShiftAdjustments`: only `action='cash_adjustment'` audit entries for
  that shift are summed (positive cash-in and negative cash-out/paid-out
  both round-trip correctly); a different action type on the same shift
  entity is excluded.
- `ComputeExpectedCash`: opening cash + cash sales + net adjustments,
  including correct sign handling on a negative (paid-out) adjustment;
  empty-shift-id validation error.

## Independent review (opus)

Verified the windowing SQL, the `json_extract`/`CAST` adjustment summation
(confirmed no float/integer drift risk since minor-unit amounts are always
whole numbers), and the expected-cash arithmetic against production line by
line — all correct. No blocking or worth-fixing findings. Three minor
coverage nitpicks noted for a future pass (inclusive-boundary timestamps
exactly on the open/close time, disambiguating `completed_at` vs `paid_at`
as the windowed field, exercising `change_given` subtraction) — none are
defects, just additional edge cases not yet exercised.

## Verification

`go build ./...`, `go test ./...`, `go test ./internal/data/... -count=3
-shuffle=on`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

## Coverage delta

8 more `pos_repo.go` functions moved from 0% to covered. Remaining 0%
count in the file: 45 (down from 53 before this batch, 111 before batch 2).
