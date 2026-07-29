# Test coverage batch 16: hold/resume (park-a-sale) API

2026-07-29

`internal/pages/hold_api.go` — hold the current basket so the till can
serve another customer, then resume it later. Persisted to `held_sales`
so it survives a till restart (offline-first). Previously zero test
coverage at either the handler layer or `internal/data/held_sales_repo.go`.

## What's covered

- `POST /api/pos/hold`: empty-basket rejection, the full happy path
  (basket snapshotted → `held_sales` row written → live basket cleared →
  `HX-Trigger: held-changed`), and the customer-name label fallback
  (label = customer name when a customer is attached, not the default
  timestamp).
- `POST /api/pos/resume`: missing id, unknown id, "basket already busy"
  refusal (holds one sale, starts a second live one, then confirms resume
  is refused and the held row survives), and the full round-trip (basket
  restored with the right SKU/qty/price, `HX-Trigger` header, held row
  consumed).
- `GET /ui/held`: chip strip lists held sales with a working resume
  button and line count; empty when nothing is held.

The hand-written `held_sales` table in the test harness was checked
column-for-column against `internal/db/migrations/002_held_sales.sql` —
exact match.

## Independent review (opus) — real gaps closed, one flaky-by-test-order bug fixed

1. **Weak round-trip check.** The restored-line assertion only checked
   `SKU == "ABC"`; a bug silently dropping/corrupting `Qty` or
   `PriceCents` through `Snapshot`/`Restore` would still have passed.
   Strengthened to assert all three.
2. **Two toast-content assertions that would pass even if the handler
   silently misbehaved.** `TestResumeHandler_RequiresID` and
   `TestResumeHandler_UnknownIDRejected` only checked for HTTP 200 (or a
   partly-dead `Contains(body, "not_found")` check — the raw locale key
   never appears in rendered output, only its English translation).
   Rewrote both to assert the real behavioral contract instead:
   `!dp.Engine.HasItems()` — nothing gets restored.
3. **Missing coverage for the customer-name label fallback** — cheap to
   add given the existing scaffolding (`Engine.SetCustomer`), added as
   `TestHoldHandler_LabelsWithCustomerNameWhenSet`.
4. **A real, review-caught test-order dependency bug**, found only once
   fixes were run against the full package (not just this file in
   isolation): the busy-toast assertion asserted the literal English copy
   ("Finish or hold the current sale first"), which only renders correctly
   if `httpx.InitI18n` had already been called by *some other test* earlier
   in the same package's test binary — this file itself never called it.
   Run alone, the toast fell back to the raw, untranslated locale key and
   the assertion failed. Fixed properly: `newHoldTestDeps` now calls
   `httpx.InitI18n` itself (matching the pattern already used by
   `audit_page_test.go`, `help_page_test.go`, etc. in this package), so
   toast-copy assertions are deterministic regardless of test run order —
   not just "usually pass because some other file happens to run first."

The delete-failure-tolerance branch in `resume` ("the sale is restored
either way; a stale row is the lesser evil") was left untested — it
would need a failing DB mid-request to exercise, which isn't cheap with
the current in-memory sqlite fixture; not a regression risk today.

## Verification

`go build ./...`, `go test ./...` (both in isolation and as part of the
full package run, to specifically catch the i18n test-order issue above),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — all pass.
