# Code Review — `money.Money` for the shifts / cash-drawer module

- **Date:** 2026-07-07
- **Branch:** `feature/money-type-shifts`
- **Author/Reviewer:** Claude Opus 4.8
- **Scope:** `internal/pos/shifts.go`, `internal/pages/shifts_api.go`.

## Summary

Extends the compiler-enforced `internal/money.Money` type (integer minor units)
to the last domain module that still carried raw `int64` cash: shifts / cash
drawer. The earlier money conversion covered the sale/tender engine and its
boundaries but left the shift-open / shift-close / cash-adjustment path on `int64`.

- `pos.ShiftInput.OpeningCash`, `pos.ShiftCloseInput.ClosingCash`, and
  `pos.CashAdjustmentInput.Amount` are now `money.Money`. All `pos` domain input
  structs (`SaleInput`, `PaymentInput`, and now the shift inputs) are uniformly
  `Money`, so the compiler blocks passing a quantity/rate where cash is expected.
- Conversions happen only at the two established boundaries per CLAUDE.md /
  coding-standards:
  - **DB seam** — repo methods (`InsertShift`, `UpdateShiftClose`,
    `ComputeExpectedCash`, `LoadShiftForClose`) keep their `int64` signatures;
    the domain converts with `.Minor()` outbound and `money.FromMinor()` for the
    repo's returned `expectedCash`.
  - **Wire DTOs** — the JSON request/response structs in `shifts_api.go`
    (`ShiftOpenRequest`, `ShiftCloseRequest`, `CashAdjustmentRequest`, and the
    responses) stay `int64`; the handlers bridge to the domain with
    `money.FromMinor(req.…)`. This mirrors the existing `pos_api.go` /
    `inventory_api.go` convention (int64 JSON DTOs, `Money` internally).

## Notes / decisions

- **Wire format unchanged.** `Money` is a named `int64` that marshals as the same
  integer, and validations (`< 0`, `== 0`) use untyped constants, so behavior and
  JSON are identical. The shift `*_test.go` literals (`OpeningCash: 10000`) compile
  unchanged for the same reason — no test edits needed.
- **Variance** in `CloseShift` is now computed as `Money - Money`
  (`in.ClosingCash - money.FromMinor(expectedCash)`) then `.Minor()` for the audit
  payload — type-safe subtraction rather than raw int64.
- **`pos.ComputeExpectedCash` (package fn) left as `int64`.** It is a thin
  pass-through to the repo and is consumed by the API layer directly into the
  int64 response DTO; wrapping it in `Money` would only add conversions at both
  ends with no arithmetic in between. The DB seam is the right place for it.
- Audit-log `payload` values use `.Minor()` so persisted amounts are explicit
  `int64` (unchanged from before).

## Verification

`go build ./...`, `go vet`, `bash scripts/ci/guard-data-access.sh` (no inline SQL),
and `go test ./...` — all green.
