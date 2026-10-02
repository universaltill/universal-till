# Review: a completed sale can no longer be reopened (ut-docs#3368)

- **Date:** 2026-10-02
- **Lane:** lane:cloud-54 (built on the session model; reviewed by Fable, a different model)
- **Card:** ut-docs#3368 — found by the UK electronic sales suppression self-audit (ut-docs#3339, `reference/uk-compliance.md` §2)

## What shipped

`POST /api/pos/sale/status` accepted any target status for any existing
sale, so `{"status":"open"}` on a completed sale dropped it out of every
report and Z aggregate (they filter `status = 'completed'`) without
counting it as a cancellation, with no role gate.

- `internal/pos/sales.go`: `saleStatusTransitionAllowed` — open/parked →
  open|parked|voided; completed → voided only; voided/refunded/unknown →
  nothing (fail closed). `refunded` and `completed` are never valid
  targets. Refusal = `ErrSaleStatusTransition`, nothing written, no audit
  row. `UpdateSaleStatus` gained `blockedActorID` (manager-PIN approvals
  audit both approver and refused operator via `InsertAuditElevated`); the
  audit payload records `from`.
- `internal/data/pos_repo.go`: `SaleStatus`; `UpdateSaleStatus` is a
  compare-and-set on the status the domain checked (`ErrSaleStatusChanged`
  when it moved; not-found still `ErrSaleNotFound`).
- `internal/pages/pos_api.go`: `voided` goes through
  `checkOrElevate(d, r, "refund", override_pin)` (403 when refused);
  `ErrSaleStatusTransition` → 409 `pos.error.sale_status_transition`
  (en/ar/fa/tr; de/es/pt in their pack repos).
- CHANGELOG (Fixed); ut-docs `reference/uk-compliance.md` §2 marks the gap
  closed.

## Findings (Fable, round 1)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `completed → refunded` was still allowed, but `refunded` is counted by no report (real refunds are `sale_type='return'` sales; `pos_api.go` documents the status as unreachable) — a manager-gated suppression path | **Fixed**: `refunded` refused as a target |
| 2 | should-fix | open/parked → `completed` let an API call "complete" a sale with no payments, signing or receipt | **Fixed**: `completed` refused as a target (only tender completes a sale) |
| 3 | nit | an unknown status string answers 400 `pos.error.server` | Accepted: API-only route, localized, no leak |
| 4 | nit | review record missing from the WIP snapshot | Fixed (this file) |

Reviewer verified: no production/e2e/JS/mobile caller of the route (only
the demo-mode allow-list), so gating `voided` behind `refund` breaks no
cashier flow; `refund` (refund_page.go precedent) is the right whole-sale
permission, `void_comp_waste` is line-level; compare-and-set correct inside
the immediate-lock tx; audit FKs use real user ids; tests are not
false-passes.

## Round 2 (Fable, scoped to the round-1 fixes)

Rule, tests (20 transition cases; 14 fail with the rule forced open),
en/ar/fa/tr strings, CHANGELOG and the de/es/pt pack strings checked —
no blockers or should-fixes. One nit fixed: a comment on why
`refunded`/`completed` stay in the input whitelist (so they get the
localized 409, not a 400).

## Verification

- TDD: `TestUpdateSaleStatus_TransitionRules` fails on every refusal case
  with the rule forced to `return true`, passes restored (re-verified
  independently by the reviewer in its own worktree).
- Handler tests (`pos_status_transition_test.go`) use real users and PINs:
  cashier without PIN → 403, cashier PIN → 403, manager PIN → 204 with
  approver + blocked actor audited; reopen/refund/complete of a completed
  sale → 409 with status and audit unchanged.
- Gate: `go build ./...`, `go test ./...`, `golangci-lint run ./...` (0
  issues), `guard-i18n.sh`, CI guard set.
- No UI surface touched (API-only route), so no UX pass or help-topic
  change.

## Verdict

Safe to merge (round 2 verdict).
