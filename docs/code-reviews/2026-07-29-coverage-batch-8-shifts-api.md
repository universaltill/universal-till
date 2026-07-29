# Test coverage batch 8: shifts API handlers

2026-07-29

First batch of `internal/pages` handler coverage (the list of untested
handler-package functions turned out much larger than initially scoped —
~90 functions across ~30 files — so this is being worked in risk-ordered
sub-batches, starting with the money-critical ones). `shifts_api.go`'s
`OpenShift`, `CloseShift`, `RecordCashAdjustment` — the HTTP layer wrapping
the shift lifecycle covered at the repo level in batch 4.

## What changed

- `internal/pages/shifts_api_test.go` (new): open/close/adjustment happy
  paths, cashier-defaults-to-session-user, one-open-shift-per-register
  enforcement, full validation coverage, and the expected-cash/variance
  arithmetic end-to-end through a real close (including a cash payout
  correctly reducing expected cash).
- `internal/pages/ui_smoke_test.go`: added a `shifts` table to the shared
  `seedForPages` fixture (matches the real migration exactly), missing
  until now.

## Verification note worth keeping

`ShiftCloseResponse.Variance` is `json:"variance,omitempty"` — a genuinely
zero variance is **omitted from the JSON entirely**, not printed as
`"variance":0`. First draft asserted the wrong thing; fixed to assert the
key's absence instead, and confirmed via a companion assertion
(`expected_cash` present, non-zero) that the omission is really the
omitempty zero-value behavior, not an unrelated serialization gap.

## Independent review (opus)

Verified the money-math tests against production line by line: expected-
cash = opening + cash sales + adjustments (matches `ComputeExpectedCash`),
variance = closing − expected (matches the handler), payout amounts flow
correctly through `SumShiftAdjustments`'s `json_extract`. Confirmed the
one-shift-per-register enforcement and its (current) 500 status code
faithfully match production — noted as a nitpick, not a bug: the handler
funnels every `pos.OpenShift` error through 500 with no 409 branch for
this specific business-rule conflict, which is a legitimate future UX
improvement but out of scope for a coverage backfill.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

## Coverage delta

`internal/pages/shifts_api.go`: 0% → 50-100% per function.

## Remaining internal/pages backlog

~85 more untested functions across ~29 files, prioritized for future
batches: sync (`sync_api.go`, `sync_sales.go`, `sync_assets.go`,
`sync_admin.go` — offline-first is ADR-0003's core guarantee), refunds
(`refund_page.go` — directly related to bugs already found this session),
print/invoice (`print_api.go`, `invoice_page.go`, `kitchen_print.go`,
`receipt_designer.go`), EOD (`eod_api.go`), then lower-risk registration/
admin-UI wrappers (ai_api, ask_api, backup_api, cloudsync_wire, designer,
marketplace_v1_stub, menu_page, plugin_settings_page, reports_page,
settings_page, suggestions_api, translations_page, update_api) and
`internal/pages/common` (deps.go, state.go — small, foundational, used
everywhere).
